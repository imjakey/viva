package video

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"io"
	"time"
)

// gifDecoder shows an animated GIF. Every frame is drawn over the one before
// it, or over whatever that frame's disposal left, into a single picture
// that holds the bytes of the frame as it stands.
type gifDecoder struct {
	g      *gif.GIF
	delays []time.Duration
	next   int

	width, height, stride int
	picture               []byte // the composed frame, premultiplied BGRA
	before                []byte // the picture before the current frame drew
	hasBefore             bool   // before holds that picture
	colors                [256][4]byte
}

// NewGIFDecoder returns a decoder of an animated GIF's frames, for
// NewDecoder, as browsers show them: a frame of 10 ms or less shows for
// 100 ms. The GIF decodes into memory at once, its frames' pixels a byte
// each, and each frame is composed as it plays. A GIF that ends early, with
// no trailer or cut off in a frame, plays the frames it has whole, and a
// pixel past its frame's colors is black. The player's SetLoop, not the
// GIF's loop count, says whether it plays again.
func NewGIFDecoder(data []byte) (Decoder, error) {
	if err := checkGIFDimensions(data); err != nil {
		return nil, err
	}
	g, err := decodeGIF(data)
	if err != nil {
		return nil, err
	}
	w, h := g.Config.Width, g.Config.Height
	if w <= 0 || h <= 0 {
		// No logical screen: the frames' bounds are the picture's.
		for _, f := range g.Image {
			if m := f.Bounds().Max.X; m > w {
				w = m
			}
			if m := f.Bounds().Max.Y; m > h {
				h = m
			}
		}
	}
	if w <= 0 || h <= 0 || uint64(w)*uint64(h) > maxDecoderPixels {
		return nil, errors.New("video: a GIF of an invalid size")
	}
	d := &gifDecoder{g: g, width: w, height: h, stride: 4 * w}
	d.picture = make([]byte, d.stride*h)
	d.before = make([]byte, len(d.picture))
	d.delays = make([]time.Duration, len(g.Image))
	for i := range d.delays {
		d.delays[i] = gifFrameDelay(g, i)
	}
	return d, nil
}

// checkGIFDimensions rejects GIFs whose logical screen or frame descriptors
// exceed maxDecoderPixels before image/gif allocates frame buffers.
func checkGIFDimensions(data []byte) error {
	const headerAndScreen = 13
	if len(data) < headerAndScreen || (!bytes.HasPrefix(data, []byte("GIF87a")) && !bytes.HasPrefix(data, []byte("GIF89a"))) {
		return nil
	}
	screenW := uint64(binary.LittleEndian.Uint16(data[6:8]))
	screenH := uint64(binary.LittleEndian.Uint16(data[8:10]))
	if screenW*screenH > maxDecoderPixels {
		return errors.New("video: a GIF of an invalid size")
	}
	off := headerAndScreen
	if data[10]&0x80 != 0 {
		off += 3 << (data[10]&7 + 1)
	}
	for off < len(data) {
		switch data[off] {
		case 0x21:
			if off+2 > len(data) {
				return nil
			}
			end, ok := skipSubBlocks(data, off+2)
			if !ok {
				return nil
			}
			off = end
		case 0x2c:
			if off+10 > len(data) {
				return nil
			}
			fw := uint64(binary.LittleEndian.Uint16(data[off+5 : off+7]))
			fh := uint64(binary.LittleEndian.Uint16(data[off+7 : off+9]))
			if fw*fh > maxDecoderPixels {
				return errors.New("video: a GIF of an invalid size")
			}
			flags := data[off+9]
			off += 10
			if flags&0x80 != 0 {
				off += 3 << (flags&7 + 1)
			}
			if off >= len(data) {
				return nil
			}
			end, ok := skipSubBlocks(data, off+1)
			if !ok {
				return nil
			}
			off = end
		default:
			return nil
		}
	}
	return nil
}

// decodeGIF decodes a GIF, from the part of the file that is whole when the
// file as it is does not decode.
func decodeGIF(data []byte) (*gif.GIF, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err == nil {
		return g, nil
	}
	whole := salvageGIF(data)
	if whole == nil {
		return nil, err
	}
	return gif.DecodeAll(bytes.NewReader(whole))
}

// salvageGIF rebuilds a GIF from the part of it that is whole, for a file
// that ends early or whose pixels point past its colors. Every color table
// is widened to its full 256 entries, black where the file named no color,
// so the pixels a short table cannot name read black rather than failing the
// decode, as Chrome draws them. What comes back is the last complete frame
// and the trailer; nil says that no frame is complete.
func salvageGIF(data []byte) []byte {
	const headerAndScreen = 13
	if len(data) < headerAndScreen {
		return nil
	}
	out := append([]byte(nil), data[:headerAndScreen]...)
	off := headerAndScreen
	if out[10]&0x80 != 0 { // a global color table follows
		var ok bool
		if off, ok = widenColorTable(&out, data, off, 10); !ok {
			return nil
		}
	}
	lastFrame := -1
	for off < len(data) {
		switch data[off] {
		case 0x21: // an extension: its label, then data sub-blocks
			if off+2 > len(data) {
				off = len(data)
				continue
			}
			end, ok := skipSubBlocks(data, off+2)
			if !ok {
				off = len(data)
				continue
			}
			out = append(out, data[off:end]...)
			off = end
		case 0x2c: // a frame: descriptor, color table, code size, sub-blocks
			if off+10 > len(data) {
				off = len(data)
				continue
			}
			out = append(out, data[off:off+10]...)
			next, flags := off+10, len(out)-1
			if out[flags]&0x80 != 0 { // a local color table follows
				var ok bool
				if next, ok = widenColorTable(&out, data, next, flags); !ok {
					off = len(data)
					continue
				}
			}
			if next >= len(data) {
				off = len(data)
				continue
			}
			end, ok := skipSubBlocks(data, next+1) // past the minimum code size
			if !ok {
				off = len(data)
				continue
			}
			out = append(out, data[next:end]...)
			off = end
			lastFrame = len(out)
		default: // the trailer, or bytes that are no GIF: stop here
			off = len(data)
		}
	}
	if lastFrame < 0 {
		return nil
	}
	return append(out[:lastFrame:lastFrame], 0x3b)
}

// widenColorTable copies the color table at off, whose size bits are in the
// byte at flags, padding it to 256 colors of black. It returns where the
// table ended in data.
func widenColorTable(out *[]byte, data []byte, off, flags int) (int, bool) {
	bytes := 3 << ((*out)[flags]&7 + 1)
	if off+bytes > len(data) {
		return 0, false
	}
	(*out)[flags] |= 7
	*out = append(*out, data[off:off+bytes]...)
	*out = append(*out, make([]byte, 3*256-bytes)...)
	return off + bytes, true
}

// skipSubBlocks returns the offset just past the empty sub-block that ends
// the chain beginning at off, or false when the data ends inside it.
func skipSubBlocks(data []byte, off int) (int, bool) {
	for off < len(data) {
		n := int(data[off])
		off++
		if n == 0 {
			return off, true
		}
		off += n
	}
	return 0, false
}

func (d *gifDecoder) Next() (Frame, error) {
	if d.next >= len(d.g.Image) {
		return Frame{}, io.EOF
	}
	d.advance()
	return Frame{
		Pix:      d.picture,
		Width:    d.width,
		Height:   d.height,
		Stride:   d.stride,
		Duration: d.delays[d.next-1],
	}, nil
}

// advance undoes the frame before the next as its disposal says, then draws
// the next over the picture.
func (d *gifDecoder) advance() {
	i := d.next
	if i > 0 {
		d.undo(i - 1)
	}
	d.hasBefore = d.disposal(i) == gif.DisposalPrevious
	if d.hasBefore {
		copy(d.before, d.picture)
	}
	d.paint(d.g.Image[i])
	d.next++
}

// disposal is how frame i goes before the next draws.
func (d *gifDecoder) disposal(i int) byte {
	if i < len(d.g.Disposal) {
		return d.g.Disposal[i]
	}
	return 0
}

// undo takes frame i back as its disposal says: it clears the frame's
// rectangle (DisposalBackground, to transparent rather than the background
// color, as browsers clear it), restores what was under it
// (DisposalPrevious), or leaves it.
func (d *gifDecoder) undo(i int) {
	switch d.disposal(i) {
	case gif.DisposalBackground:
		d.clear(d.g.Image[i].Bounds())
	case gif.DisposalPrevious:
		if d.hasBefore {
			copy(d.picture, d.before)
		}
	}
}

// clear makes a rectangle of the picture transparent.
func (d *gifDecoder) clear(r image.Rectangle) {
	r = r.Intersect(image.Rect(0, 0, d.width, d.height))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		clear(d.picture[y*d.stride+r.Min.X*4 : y*d.stride+r.Max.X*4])
	}
}

// paint draws a frame over the picture at its place. Its colors are written
// out premultiplied in BGRA, and a transparent pixel leaves what is under it.
func (d *gifDecoder) paint(f *image.Paletted) {
	for i := range d.colors {
		d.colors[i] = [4]byte{}
	}
	for i, c := range f.Palette {
		if i == len(d.colors) {
			break
		}
		r, g, b, a := c.RGBA() // premultiplied
		d.colors[i] = [4]byte{byte(b >> 8), byte(g >> 8), byte(r >> 8), byte(a >> 8)}
	}
	r := f.Bounds().Intersect(image.Rect(0, 0, d.width, d.height))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := f.Pix[f.PixOffset(r.Min.X, y):f.PixOffset(r.Max.X, y)]
		out := d.picture[y*d.stride+r.Min.X*4:]
		for x, i := range row {
			c := d.colors[i]
			if c[3] == 0 {
				continue
			}
			copy(out[x*4:x*4+4], c[:])
		}
	}
}

func (d *gifDecoder) Rewind() error {
	d.reset()
	return nil
}

// reset takes the picture back to its state before the first frame.
func (d *gifDecoder) reset() {
	d.next = 0
	d.hasBefore = false
	clear(d.picture)
	clear(d.before)
}

// Seek starts the animation at to, composing the frames before the one that
// shows then. It returns when that frame starts.
func (d *gifDecoder) Seek(to time.Duration) (time.Duration, error) {
	if to < 0 {
		to = 0
	}
	d.reset()
	var start time.Duration
	for d.next+1 < len(d.delays) && start+d.delays[d.next] <= to {
		start += d.delays[d.next]
		d.advance()
	}
	return start, nil
}

// Duration is the length of the animation, for the player's Duration.
func (d *gifDecoder) Duration() time.Duration {
	var total time.Duration
	for _, delay := range d.delays {
		total += delay
	}
	return total
}

// gifFrameDelay is how long frame i shows. A GIF counts delays in hundredths
// of a second; a frame of a hundredth or less shows for a tenth instead, as
// browsers show it, so that no GIF flashes through its frames.
func gifFrameDelay(g *gif.GIF, i int) time.Duration {
	if i < len(g.Delay) && g.Delay[i] > 1 {
		return time.Duration(g.Delay[i]) * 10 * time.Millisecond
	}
	return 100 * time.Millisecond
}
