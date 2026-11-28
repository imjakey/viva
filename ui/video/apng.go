package video

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"time"
)

// pngSignature begins every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

const (
	apngDisposeBackground = 1
	apngDisposePrevious   = 2
	apngBlendOver         = 1
)

// apngDecoder plays an animated PNG. It reads the whole file at once but
// keeps each frame's pixels compressed, as they came; a frame is decoded and
// composed over the picture as it comes due.
type apngDecoder struct {
	header  []byte   // the IHDR chunk's data, of the whole picture
	palette [][]byte // complete PLTE and tRNS chunks, which a frame needs
	frames  []apngFrame
	next    int

	width, height, stride int
	picture               []byte // the composed frame, premultiplied BGRA
	before                []byte // the picture before the current frame drew
	hasBefore             bool   // before holds that picture
}

// apngFrame is one frame of an animated PNG: its compressed pixels, the
// rectangle it draws, how long it shows, and how it clears (dispose) and
// draws (blend) before the next.
type apngFrame struct {
	pixels         []byte
	rect           image.Rectangle
	delay          time.Duration
	dispose, blend byte
}

// NewAPNGDecoder returns a decoder of an animated PNG's frames, for
// NewDecoder, as browsers show them: a frame of 10 ms or less shows for
// 100 ms. A PNG that is not animated is a video of one frame. Each frame
// is kept compressed, and decoded as it plays. The player's SetLoop, not
// the PNG's count of plays, says whether it plays again.
func NewAPNGDecoder(data []byte) (Decoder, error) {
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errors.New("video: not a PNG")
	}
	d := &apngDecoder{}
	if err := d.read(data); err != nil {
		return nil, err
	}
	d.stride = 4 * d.width
	d.picture = make([]byte, d.stride*d.height)
	d.before = make([]byte, len(d.picture))
	return d, nil
}

// read walks the PNG's chunks: its header and palette, then the frames the
// frame controls and image data make.
func (d *apngDecoder) read(data []byte) error {
	var (
		off          = len(pngSignature)
		animated     bool
		defaultImage []byte     // IDAT pixels of a PNG with no frame control before them
		current      *apngFrame // the frame the image data belongs to
		sawHeader    bool
		sawImage     bool
	)
	for off < len(data) {
		kind, body, raw, next, err := pngChunk(data, off)
		if err != nil {
			return err
		}
		off = next
		switch kind {
		case "IHDR":
			if len(body) != 13 {
				return errors.New("video: a PNG of a bad header")
			}
			d.header = append([]byte(nil), body...)
			d.width = int(binary.BigEndian.Uint32(body))
			d.height = int(binary.BigEndian.Uint32(body[4:]))
			if d.width <= 0 || d.height <= 0 || uint64(d.width)*uint64(d.height) > maxDecoderPixels {
				return errors.New("video: a PNG of an invalid size")
			}
			sawHeader = true
		case "PLTE", "tRNS":
			d.palette = append(d.palette, append([]byte(nil), raw...))
		case "acTL":
			animated = true
		case "fcTL":
			if !sawHeader {
				return errors.New("video: an APNG of a bad frame control")
			}
			if current != nil && len(current.pixels) == 0 {
				return errors.New("video: an APNG frame of no data")
			}
			f, err := readFrameControl(body, d.width, d.height)
			if err != nil {
				return err
			}
			if len(d.frames) == 0 && f.dispose == apngDisposePrevious {
				f.dispose = apngDisposeBackground // there is nothing before it
			}
			d.frames = append(d.frames, f)
			current = &d.frames[len(d.frames)-1]
		case "IDAT":
			sawImage = true
			switch {
			case current != nil:
				current.pixels = append(current.pixels, body...)
			case !animated:
				defaultImage = append(defaultImage, body...)
			}
		case "fdAT":
			if current != nil && len(body) >= 4 {
				current.pixels = append(current.pixels, body[4:]...)
			}
		case "IEND":
			off = len(data)
		}
	}
	if !sawHeader || !sawImage {
		return errors.New("video: a PNG of no image")
	}
	if current != nil && len(current.pixels) == 0 {
		return errors.New("video: an APNG frame of no data")
	}
	if len(d.frames) == 0 {
		if len(defaultImage) == 0 {
			return errors.New("video: a PNG of no image")
		}
		d.frames = []apngFrame{{
			pixels: defaultImage,
			rect:   image.Rect(0, 0, d.width, d.height),
			delay:  100 * time.Millisecond,
		}}
	}
	return nil
}

// pngChunk reads the chunk at off: its kind, its body, the bytes it spans,
// and where the next chunk starts.
func pngChunk(data []byte, off int) (kind string, body, raw []byte, next int, err error) {
	if off+12 > len(data) {
		return "", nil, nil, 0, errors.New("video: a truncated PNG")
	}
	n := uint64(binary.BigEndian.Uint32(data[off:]))
	if n > uint64(len(data)-off-12) {
		return "", nil, nil, 0, errors.New("video: a truncated PNG")
	}
	kind = string(data[off+4 : off+8])
	body = data[off+8 : off+8+int(n)]
	raw = data[off : off+12+int(n)]
	return kind, body, raw, off + 12 + int(n), nil
}

// readFrameControl reads an fcTL chunk: the rectangle the frame draws, how
// long it shows, and how it clears and blends.
func readFrameControl(body []byte, width, height int) (apngFrame, error) {
	if len(body) != 26 {
		return apngFrame{}, errors.New("video: an APNG of a bad frame control")
	}
	w := binary.BigEndian.Uint32(body[4:])
	h := binary.BigEndian.Uint32(body[8:])
	x := binary.BigEndian.Uint32(body[12:])
	y := binary.BigEndian.Uint32(body[16:])
	if w == 0 || h == 0 || uint64(x)+uint64(w) > uint64(width) || uint64(y)+uint64(h) > uint64(height) {
		return apngFrame{}, errors.New("video: an APNG frame out of its picture")
	}
	num, den := binary.BigEndian.Uint16(body[20:]), binary.BigEndian.Uint16(body[22:])
	if den == 0 {
		den = 100 // the numerator counts hundredths of a second
	}
	delay := time.Duration(num) * time.Second / time.Duration(den)
	if delay <= 10*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	return apngFrame{
		rect:    image.Rect(int(x), int(y), int(x)+int(w), int(y)+int(h)),
		delay:   delay,
		dispose: body[24],
		blend:   body[25],
	}, nil
}

func (d *apngDecoder) Next() (Frame, error) {
	if d.next >= len(d.frames) {
		return Frame{}, io.EOF
	}
	if err := d.advance(); err != nil {
		return Frame{}, err
	}
	return Frame{
		Pix:      d.picture,
		Width:    d.width,
		Height:   d.height,
		Stride:   d.stride,
		Duration: d.frames[d.next-1].delay,
	}, nil
}

// advance decodes the next frame and draws it over the picture, after undoing
// the frame before as its disposal says.
func (d *apngDecoder) advance() error {
	i := d.next
	f := &d.frames[i]
	img, err := png.Decode(bytes.NewReader(d.framePNG(f)))
	if err != nil {
		return err
	}
	if i > 0 {
		d.undo(i - 1)
	}
	d.hasBefore = f.dispose == apngDisposePrevious
	if d.hasBefore {
		copy(d.before, d.picture)
	}
	d.draw(img, f.rect, f.blend == apngBlendOver)
	d.next++
	return nil
}

// framePNG is the PNG a frame's pixels decode from: the picture's header at
// the frame's size, the picture's palette, and the frame's data.
func (d *apngDecoder) framePNG(f *apngFrame) []byte {
	var b bytes.Buffer
	b.Write(pngSignature)
	hdr := append([]byte(nil), d.header...)
	binary.BigEndian.PutUint32(hdr, uint32(f.rect.Dx()))
	binary.BigEndian.PutUint32(hdr[4:], uint32(f.rect.Dy()))
	writeChunk(&b, "IHDR", hdr)
	for _, chunk := range d.palette {
		b.Write(chunk)
	}
	writeChunk(&b, "IDAT", f.pixels)
	writeChunk(&b, "IEND", nil)
	return b.Bytes()
}

// draw puts a decoded frame over the picture in its rectangle, replacing what
// is under it or blending with it. Fast paths handle the common concrete
// PNG image types directly without per-pixel interface boxing.
func (d *apngDecoder) draw(img image.Image, r image.Rectangle, over bool) {
	b := img.Bounds()
	w := min(r.Dx(), b.Dx())
	h := min(r.Dy(), b.Dy())
	if w <= 0 || h <= 0 {
		return
	}
	switch src := img.(type) {
	case *image.NRGBA:
		for y := 0; y < h; y++ {
			in := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			out := d.picture[(r.Min.Y+y)*d.stride+r.Min.X*4:]
			for x := 0; x < w; x++ {
				si := x * 4
				sr, sg, sb, sa := in[si], in[si+1], in[si+2], in[si+3]
				if sa == 0 {
					if !over {
						out[si], out[si+1], out[si+2], out[si+3] = 0, 0, 0, 0
					}
					continue
				}
				if sa == 255 {
					out[si], out[si+1], out[si+2], out[si+3] = sb, sg, sr, 255
					continue
				}
				a := uint32(sa)
				pixel := [4]byte{
					byte((uint32(sb)*a + 127) / 255),
					byte((uint32(sg)*a + 127) / 255),
					byte((uint32(sr)*a + 127) / 255),
					sa,
				}
				if over {
					putOver(out[si:], pixel)
				} else {
					copy(out[si:si+4], pixel[:])
				}
			}
		}
	case *image.RGBA:
		for y := 0; y < h; y++ {
			in := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			out := d.picture[(r.Min.Y+y)*d.stride+r.Min.X*4:]
			for x := 0; x < w; x++ {
				si := x * 4
				pixel := [4]byte{in[si+2], in[si+1], in[si], in[si+3]}
				if over {
					if pixel[3] == 0 {
						continue
					}
					if pixel[3] == 255 {
						copy(out[si:si+4], pixel[:])
					} else {
						putOver(out[si:], pixel)
					}
				} else {
					copy(out[si:si+4], pixel[:])
				}
			}
		}
	case *image.Paletted:
		var colors [256][4]byte
		for i, c := range src.Palette {
			if i == len(colors) {
				break
			}
			cr, cg, cb, ca := c.RGBA()
			colors[i] = [4]byte{byte(cb >> 8), byte(cg >> 8), byte(cr >> 8), byte(ca >> 8)}
		}
		for y := 0; y < h; y++ {
			in := src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):]
			out := d.picture[(r.Min.Y+y)*d.stride+r.Min.X*4:]
			for x := 0; x < w; x++ {
				pixel := colors[in[x]]
				si := x * 4
				if over {
					if pixel[3] == 0 {
						continue
					}
					if pixel[3] == 255 {
						copy(out[si:si+4], pixel[:])
					} else {
						putOver(out[si:], pixel)
					}
				} else {
					copy(out[si:si+4], pixel[:])
				}
			}
		}
	default:
		for y := 0; y < h; y++ {
			out := d.picture[(r.Min.Y+y)*d.stride+r.Min.X*4:]
			for x := 0; x < w; x++ {
				cr, cg, cb, ca := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				pixel := [4]byte{byte(cb >> 8), byte(cg >> 8), byte(cr >> 8), byte(ca >> 8)}
				if over {
					putOver(out[x*4:], pixel)
				} else {
					copy(out[x*4:x*4+4], pixel[:])
				}
			}
		}
	}
}

// putOver puts a premultiplied pixel over the one at the start of dst.
func putOver(dst []byte, src [4]byte) {
	rest := uint32(255 - src[3])
	for i := 0; i < 4; i++ {
		dst[i] = byte(uint32(src[i]) + (uint32(dst[i])*rest+127)/255)
	}
}

// undo takes frame i back as its disposal says: it clears the frame's
// rectangle to transparent, restores what was under it, or leaves it.
func (d *apngDecoder) undo(i int) {
	switch d.frames[i].dispose {
	case apngDisposeBackground:
		d.clear(d.frames[i].rect)
	case apngDisposePrevious:
		if d.hasBefore {
			copy(d.picture, d.before)
		}
	}
}

// clear makes a rectangle of the picture transparent.
func (d *apngDecoder) clear(r image.Rectangle) {
	r = r.Intersect(image.Rect(0, 0, d.width, d.height))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		clear(d.picture[y*d.stride+r.Min.X*4 : y*d.stride+r.Max.X*4])
	}
}

func (d *apngDecoder) Rewind() error {
	d.reset()
	return nil
}

// reset takes the picture back to its state before the first frame.
func (d *apngDecoder) reset() {
	d.next = 0
	d.hasBefore = false
	clear(d.picture)
	clear(d.before)
}

// Seek starts the animation at to, composing the frames before the one that
// shows then. It returns when that frame starts.
func (d *apngDecoder) Seek(to time.Duration) (time.Duration, error) {
	if to < 0 {
		to = 0
	}
	d.reset()
	var start time.Duration
	for d.next+1 < len(d.frames) && start+d.frames[d.next].delay <= to {
		start += d.frames[d.next].delay
		if err := d.advance(); err != nil {
			return 0, err
		}
	}
	return start, nil
}

// Duration is the length of the animation, for the player's Duration.
func (d *apngDecoder) Duration() time.Duration {
	var total time.Duration
	for _, f := range d.frames {
		total += f.delay
	}
	return total
}

// writeChunk writes a PNG chunk: its length, kind, body and CRC.
func writeChunk(b *bytes.Buffer, kind string, data []byte) {
	crc := crc32.NewIEEE()
	crc.Write([]byte(kind))
	crc.Write(data)
	var head [8]byte
	binary.BigEndian.PutUint32(head[:4], uint32(len(data)))
	copy(head[4:], kind)
	b.Write(head[:])
	b.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	b.Write(sum[:])
}
