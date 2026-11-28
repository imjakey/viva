package video

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// apngFrameSpec is a frame of a test APNG: a solid w×h rectangle at x, y.
type apngFrameSpec struct {
	c                color.NRGBA
	x, y, w, h       int
	dispose, blend   byte
	delayNum, delDen uint16
}

// testAPNG builds an animated PNG of w×h of the frames given, the first the
// default image unless hidden, which adds a default image that is no frame.
func testAPNG(t *testing.T, w, h int, hidden bool, frames ...apngFrameSpec) []byte {
	t.Helper()
	encode := func(c color.NRGBA, fw, fh int) (ihdr, idat []byte) {
		img := image.NewNRGBA(image.Rect(0, 0, fw, fh))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
		}
		var b bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&b, translucent{img}); err != nil {
			t.Fatal(err)
		}
		data := b.Bytes()[8:]
		for len(data) >= 12 {
			n := binary.BigEndian.Uint32(data)
			kind, chunk := string(data[4:8]), data[8:8+n]
			switch kind {
			case "IHDR":
				ihdr = chunk
			case "IDAT":
				idat = append(idat, chunk...)
			}
			data = data[12+n:]
		}
		return ihdr, idat
	}
	var b bytes.Buffer
	b.Write(pngSignature)
	ihdr, _ := encode(color.NRGBA{}, w, h)
	ihdr = append([]byte(nil), ihdr...)
	writeChunk(&b, "IHDR", ihdr)
	actl := make([]byte, 8)
	binary.BigEndian.PutUint32(actl, uint32(len(frames)))
	writeChunk(&b, "acTL", actl)
	seq := uint32(0)
	if hidden {
		_, idat := encode(color.NRGBA{255, 255, 255, 255}, w, h)
		writeChunk(&b, "IDAT", idat)
	}
	for i, f := range frames {
		fc := make([]byte, 26)
		binary.BigEndian.PutUint32(fc, seq)
		seq++
		binary.BigEndian.PutUint32(fc[4:], uint32(f.w))
		binary.BigEndian.PutUint32(fc[8:], uint32(f.h))
		binary.BigEndian.PutUint32(fc[12:], uint32(f.x))
		binary.BigEndian.PutUint32(fc[16:], uint32(f.y))
		binary.BigEndian.PutUint16(fc[20:], f.delayNum)
		binary.BigEndian.PutUint16(fc[22:], f.delDen)
		fc[24], fc[25] = f.dispose, f.blend
		writeChunk(&b, "fcTL", fc)
		_, idat := encode(f.c, f.w, f.h)
		if i == 0 && !hidden {
			writeChunk(&b, "IDAT", idat)
			continue
		}
		fd := make([]byte, 4, 4+len(idat))
		binary.BigEndian.PutUint32(fd, seq)
		seq++
		writeChunk(&b, "fdAT", append(fd, idat...))
	}
	writeChunk(&b, "IEND", nil)
	return b.Bytes()
}

// translucent has the PNG encoder keep alpha, as every frame of an APNG is
// of the same color type.
type translucent struct{ *image.NRGBA }

func (translucent) Opaque() bool { return false }

// TestAPNGDecoderFrames checks that an animated PNG's frames draw over what
// the ones before left as their disposal and blending say, for as long as
// their delays say, that a default image before the first frame control is
// not a frame, and that a PNG not animated is a frame.
func TestAPNGDecoderFrames(t *testing.T) {
	red, green := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}
	half := color.NRGBA{0, 0, 255, 128}
	data := testAPNG(t, 4, 4, false,
		apngFrameSpec{c: red, w: 4, h: 4, delayNum: 1, delDen: 5},
		// Over the red, then cleared.
		apngFrameSpec{c: green, x: 1, y: 1, w: 2, h: 2, delayNum: 30, dispose: apngDisposeBackground},
		// Blended over what the clearing left.
		apngFrameSpec{c: half, x: 0, y: 0, w: 2, h: 2, blend: apngBlendOver, dispose: apngDisposePrevious},
		// Over what the frame before had under it, replacing it.
		apngFrameSpec{c: half, x: 3, y: 3, w: 1, h: 1, delayNum: 0},
	)
	d, err := NewAPNGDecoder(data)
	if err != nil {
		t.Fatal(err)
	}
	videored := color.RGBA{255, 0, 0, 255}
	greenRGBA := color.RGBA{0, 255, 0, 255}
	type check struct {
		x, y int
		want color.RGBA
	}
	blue := color.RGBA{0, 0, 128, 128}
	steps := []struct {
		delay  time.Duration
		checks []check
	}{
		{200 * time.Millisecond, []check{{0, 0, videored}, {3, 3, videored}}},
		{300 * time.Millisecond, []check{{1, 1, greenRGBA}, {0, 0, videored}}},
		{100 * time.Millisecond, []check{{0, 0, blend(videored, blue)}, {1, 1, blue}, {2, 2, videored}}},
		{100 * time.Millisecond, []check{{0, 0, videored}, {1, 1, color.RGBA{}}, {3, 3, blue}}},
	}
	for range 2 {
		for i, s := range steps {
			f, err := d.Next()
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			if f.Duration != s.delay {
				t.Errorf("frame %d shows for %v, want %v", i, f.Duration, s.delay)
			}
			for _, c := range s.checks {
				if got := framePixel(f, c.x, c.y); !nearRGBA(got, c.want) {
					t.Errorf("frame %d at %d,%d: %v, want %v", i, c.x, c.y, got, c.want)
				}
			}
		}
		if _, err := d.Next(); err == nil {
			t.Fatal("a frame after the last")
		}
		if err := d.Rewind(); err != nil {
			t.Fatal(err)
		}
	}
	if got := d.(durationReader).Duration(); got != 700*time.Millisecond {
		t.Errorf("duration %v, want 700ms", got)
	}

	// A default image before the first frame control is no frame.
	d, err = NewAPNGDecoder(testAPNG(t, 2, 2, true, apngFrameSpec{c: green, w: 2, h: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if f, err := d.Next(); err != nil || framePixel(f, 0, 0) != greenRGBA {
		t.Fatalf("first frame after a default image: %v", err)
	}
	if _, err := d.Next(); err == nil {
		t.Fatal("the default image was a frame")
	}

	// A PNG not animated.
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	d, err = NewAPNGDecoder(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if f, err := d.Next(); err != nil || f.Width != 3 || f.Height != 2 {
		t.Fatalf("a PNG not animated: %v", err)
	}
	if _, err := NewAPNGDecoder([]byte("GIF89a")); err == nil {
		t.Error("a GIF decoded as a PNG")
	}
}

// TestAPNGSeek checks that a seek starts at the frame showing then, the
// frames before it composed, and reports when it starts.
func TestAPNGSeek(t *testing.T) {
	red, green, blue := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{0, 0, 255, 255}
	d, err := NewAPNGDecoder(testAPNG(t, 2, 2, false,
		apngFrameSpec{c: red, w: 2, h: 2, delayNum: 10, delDen: 100},
		apngFrameSpec{c: green, w: 2, h: 2, delayNum: 10, delDen: 100},
		apngFrameSpec{c: blue, w: 2, h: 2, delayNum: 10, delDen: 100},
	))
	if err != nil {
		t.Fatal(err)
	}
	seeker, ok := d.(Seeker)
	if !ok {
		t.Fatal("the APNG decoder does not seek")
	}
	for _, test := range []struct {
		to    time.Duration
		start time.Duration
		want  color.RGBA
	}{
		{0, 0, color.RGBA{255, 0, 0, 255}},
		{250 * time.Millisecond, 200 * time.Millisecond, color.RGBA{0, 0, 255, 255}},
		{time.Hour, 200 * time.Millisecond, color.RGBA{0, 0, 255, 255}},
	} {
		start, err := seeker.Seek(test.to)
		if err != nil {
			t.Fatalf("Seek(%v): %v", test.to, err)
		}
		if start != test.start {
			t.Fatalf("Seek(%v) starts at %v, want %v", test.to, start, test.start)
		}
		f, err := d.Next()
		if err != nil {
			t.Fatalf("Seek(%v): %v", test.to, err)
		}
		if got := framePixel(f, 0, 0); got != test.want {
			t.Fatalf("Seek(%v) shows %v, want %v", test.to, got, test.want)
		}
	}
}

// TestAPNGDecoderDimensions checks that a PNG whose picture is empty or too
// large for its pixel buffer returns an error instead of panicking.
func TestAPNGDecoderDimensions(t *testing.T) {
	var plain bytes.Buffer
	if err := png.Encode(&plain, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, src := range []struct {
		name string
		data []byte
	}{
		{"plain", plain.Bytes()},
		{"animated", testAPNG(t, 1, 1, false, apngFrameSpec{w: 1, h: 1})},
	} {
		for _, size := range []struct {
			name string
			w, h uint32
		}{
			{"zero width", 0, 1},
			{"zero height", 1, 0},
			{"exceeds pixel cap", 10000, 10000},
			{"pixel buffer overflow", 0x7fffffff, 0x7fffffff},
			{"largest dimensions", 0xffffffff, 0xffffffff},
		} {
			t.Run(src.name+"/"+size.name, func(t *testing.T) {
				// Replace only IHDR, including its checksum; keep the frames.
				hdr := append([]byte(nil), src.data[16:29]...)
				binary.BigEndian.PutUint32(hdr, size.w)
				binary.BigEndian.PutUint32(hdr[4:], size.h)
				var b bytes.Buffer
				b.Write(pngSignature)
				writeChunk(&b, "IHDR", hdr)
				b.Write(src.data[33:])
				if _, err := NewAPNGDecoder(b.Bytes()); err == nil {
					t.Fatal("a PNG of an invalid size decoded")
				}
			})
		}
	}
}

// blend is src, premultiplied, over dst.
func blend(dst, src color.RGBA) color.RGBA {
	k := 255 - uint32(src.A)
	f := func(d, s uint8) uint8 { return uint8(uint32(s) + (uint32(d)*k+127)/255) }
	return color.RGBA{f(dst.R, src.R), f(dst.G, src.G), f(dst.B, src.B), f(dst.A, src.A)}
}

func nearRGBA(a, b color.RGBA) bool {
	d := func(x, y uint8) bool { return x-y <= 1 || y-x <= 1 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

// TestAPNGDrawFastPaths verifies that the concrete NRGBA, RGBA and Paletted
// fast paths match the generic color.RGBA compositing behavior.
func TestAPNGDrawFastPaths(t *testing.T) {
	base := color.RGBA{200, 40, 20, 255}
	half := color.NRGBA{20, 100, 220, 128}
	halfPremul := color.RGBA{
		R: byte((uint32(half.R)*uint32(half.A) + 127) / 255),
		G: byte((uint32(half.G)*uint32(half.A) + 127) / 255),
		B: byte((uint32(half.B)*uint32(half.A) + 127) / 255),
		A: half.A,
	}
	wantOver := blend(base, halfPremul)

	makeDec := func() *apngDecoder {
		d := &apngDecoder{width: 2, height: 2, stride: 8}
		d.picture = make([]byte, 16)
		for i := 0; i < len(d.picture); i += 4 {
			d.picture[i], d.picture[i+1], d.picture[i+2], d.picture[i+3] = base.B, base.G, base.R, base.A
		}
		return d
	}

	nrgba := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	nrgba.SetNRGBA(0, 0, half)
	rgba := image.NewRGBA(image.Rect(0, 0, 2, 2))
	rgba.SetRGBA(0, 0, halfPremul)
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.NRGBA{}, half})
	pal.SetColorIndex(0, 0, 1)

	for _, tc := range []struct {
		name string
		img  image.Image
	}{
		{"NRGBA", nrgba},
		{"RGBA", rgba},
		{"Paletted", pal},
	} {
		d := makeDec()
		d.draw(tc.img, image.Rect(0, 0, 2, 2), true)
		f := Frame{Pix: d.picture, Width: 2, Height: 2, Stride: 8}
		if got := framePixel(f, 0, 0); !nearRGBA(got, wantOver) {
			t.Errorf("%s over at (0,0) = %v, want %v", tc.name, got, wantOver)
		}
		if got := framePixel(f, 1, 1); got != base {
			t.Errorf("%s over transparent at (1,1) = %v, want %v", tc.name, got, base)
		}
		d = makeDec()
		d.draw(tc.img, image.Rect(0, 0, 2, 2), false)
		f = Frame{Pix: d.picture, Width: 2, Height: 2, Stride: 8}
		if got := framePixel(f, 0, 0); !nearRGBA(got, halfPremul) {
			t.Errorf("%s replace at (0,0) = %v, want %v", tc.name, got, halfPremul)
		}
		if got := framePixel(f, 1, 1); got != (color.RGBA{}) {
			t.Errorf("%s replace transparent at (1,1) = %v, want zero", tc.name, got)
		}
	}
}
