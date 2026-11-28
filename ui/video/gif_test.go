package video

import (
	"bytes"
	"compress/lzw"
	"image"
	"image/color"
	"image/gif"
	"io"
	"testing"
	"time"
)

var (
	gifRed   = color.RGBA{255, 0, 0, 255}
	gifGreen = color.RGBA{0, 255, 0, 255}
	gifBlue  = color.RGBA{0, 0, 255, 255}
	gifWhite = color.RGBA{255, 255, 255, 255}
)

// solidGIF returns a GIF of 4×4 frames of solid colors, each showing for
// delay hundredths of a second.
func solidGIF(t *testing.T, delay int, colors ...color.RGBA) []byte {
	t.Helper()
	g := &gif.GIF{Config: image.Config{Width: 4, Height: 4}}
	for _, c := range colors {
		f := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{c})
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// framePixel reads a frame's pixel, its BGRA bytes back as a color.
func framePixel(f Frame, x, y int) color.RGBA {
	i := y*f.Stride + x*4
	return color.RGBA{R: f.Pix[i+2], G: f.Pix[i+1], B: f.Pix[i], A: f.Pix[i+3]}
}

// TestGIFDecoderFrames checks that a GIF's frames draw over what the ones
// before left as their disposal says, with their transparent pixels showing
// what is under them, for as long as their delays say, and that the decoder
// starts over after Rewind.
func TestGIFDecoderFrames(t *testing.T) {
	transparent := color.RGBA{}
	full := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{gifRed})
	// Green over the top left, but for a transparent pixel at 1,1.
	corner := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{gifGreen, transparent})
	corner.SetColorIndex(1, 1, 1)
	low := image.NewPaletted(image.Rect(2, 2, 4, 4), color.Palette{gifBlue})
	dot := image.NewPaletted(image.Rect(3, 0, 4, 1), color.Palette{gifWhite})
	g := &gif.GIF{
		Config:   image.Config{Width: 4, Height: 4},
		Image:    []*image.Paletted{full, corner, low, dot},
		Delay:    []int{0, 5, 20, 1},
		Disposal: []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	d, err := NewGIFDecoder(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	type px struct {
		x, y int
		c    color.RGBA
	}
	frames := []struct {
		d    time.Duration
		want []px
	}{
		{100 * time.Millisecond, []px{{0, 0, gifRed}, {3, 3, gifRed}}},
		{50 * time.Millisecond, []px{{0, 0, gifGreen}, {1, 1, gifRed}, {3, 3, gifRed}}},
		// The green corner went, leaving nothing; blue covers the bottom
		// right.
		{200 * time.Millisecond, []px{{0, 0, transparent}, {1, 1, transparent}, {3, 3, gifBlue}, {3, 0, gifRed}}},
		// The blue went back to what was under it.
		{100 * time.Millisecond, []px{{3, 0, gifWhite}, {3, 3, gifRed}, {0, 0, transparent}}},
	}
	check := func(round int) {
		for i, want := range frames {
			f, err := d.Next()
			if err != nil {
				t.Fatalf("round %d, frame %d: %v", round, i, err)
			}
			if f.Duration != want.d {
				t.Errorf("round %d, frame %d shows for %v, want %v", round, i, f.Duration, want.d)
			}
			for _, p := range want.want {
				if got := framePixel(f, p.x, p.y); got != p.c {
					t.Errorf("round %d, frame %d at %d,%d: %v, want %v", round, i, p.x, p.y, got, p.c)
				}
			}
		}
		if _, err := d.Next(); err != io.EOF {
			t.Errorf("round %d: after the last frame, %v", round, err)
		}
	}
	check(1)
	if err := d.Rewind(); err != nil {
		t.Fatal(err)
	}
	check(2)

	if _, err := NewGIFDecoder([]byte("GIF89a, not quite")); err == nil {
		t.Error("data that is no GIF decoded")
	}
}

// TestGIFSeek checks that a seek starts at the frame showing then, composed
// over the frames before it, and that the decoder knows the animation's
// length.
func TestGIFSeek(t *testing.T) {
	d, err := NewGIFDecoder(solidGIF(t, 10, gifRed, gifGreen, gifBlue))
	if err != nil {
		t.Fatal(err)
	}
	seeker, ok := d.(Seeker)
	if !ok {
		t.Fatal("the GIF decoder does not seek")
	}
	reader, ok := d.(durationReader)
	if !ok {
		t.Fatal("the GIF decoder does not know its duration")
	}
	if got := reader.Duration(); got != 300*time.Millisecond {
		t.Fatalf("Duration = %v, want 300ms", got)
	}

	for _, test := range []struct {
		to    time.Duration
		start time.Duration
		want  color.RGBA
	}{
		{0, 0, gifRed},
		{150 * time.Millisecond, 100 * time.Millisecond, gifGreen},
		{250 * time.Millisecond, 200 * time.Millisecond, gifBlue},
		{time.Hour, 200 * time.Millisecond, gifBlue},
		{-time.Second, 0, gifRed},
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
		if got := framePixel(f, 1, 1); got != test.want {
			t.Fatalf("Seek(%v) shows %v, want %v", test.to, got, test.want)
		}
	}
}

// TestGIFDecoderEndsEarly checks that a GIF that ends early, with no trailer
// or cut off in a frame, plays the frames it has whole, as browsers do, and
// that one with no whole frame is an error.
func TestGIFDecoderEndsEarly(t *testing.T) {
	data := solidGIF(t, 10, gifRed, gifGreen, gifBlue)
	for _, tc := range []struct {
		name string
		data []byte
		want []color.RGBA
	}{
		{"no trailer", data[:len(data)-1], []color.RGBA{gifRed, gifGreen, gifBlue}},
		// Without the last frame's block terminator and a byte of its
		// pixels.
		{"cut off in the last frame", data[:len(data)-3], []color.RGBA{gifRed, gifGreen}},
	} {
		d, err := NewGIFDecoder(tc.data)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		for i, want := range tc.want {
			f, err := d.Next()
			if err != nil {
				t.Fatalf("%s, frame %d: %v", tc.name, i, err)
			}
			if got := framePixel(f, 1, 1); got != want {
				t.Errorf("%s, frame %d: %v, want %v", tc.name, i, got, want)
			}
		}
		if _, err := d.Next(); err != io.EOF {
			t.Errorf("%s: after %d frames, %v", tc.name, len(tc.want), err)
		}
	}
	if _, err := NewGIFDecoder(data[:40]); err == nil {
		t.Error("a GIF cut off in its first frame decoded")
	}
}

// TestGIFDecoderPixelsPastColors checks that a GIF whose pixels point past
// its color table plays, those pixels black, as Chrome draws them, where
// image/gif decodes none of it.
func TestGIFDecoderPixelsPastColors(t *testing.T) {
	// A 2×2 frame of pixels 0 to 3, its color table red and green.
	var px bytes.Buffer
	w := lzw.NewWriter(&px, lzw.LSB, 2)
	w.Write([]byte{0, 1, 2, 3})
	w.Close()
	frame := func(table []byte) []byte {
		b := []byte{0x2c, 0, 0, 0, 0, 2, 0, 2, 0, 0}
		if table != nil {
			b[9] = 0x80
			b = append(b, table...)
		}
		b = append(b, 2, byte(px.Len()))
		b = append(b, px.Bytes()...)
		return append(b, 0)
	}
	colors := []byte{255, 0, 0, 0, 255, 0}
	screen := []byte("GIF89a\x02\x00\x02\x00\x00\x00\x00")
	global := append(append([]byte{}, screen...), colors...)
	global[10] = 0x80
	black := color.RGBA{0, 0, 0, 255}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"global color table", append(append(global, frame(nil)...), 0x3b)},
		{"local color table", append(append(append([]byte{}, screen...), frame(colors)...), 0x3b)},
	} {
		d, err := NewGIFDecoder(tc.data)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		f, err := d.Next()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for i, want := range []color.RGBA{gifRed, gifGreen, black, black} {
			if got := framePixel(f, i%2, i/2); got != want {
				t.Errorf("%s: pixel %d is %v, want %v", tc.name, i, got, want)
			}
		}
	}
}

// TestGIFDecoderDimensions rejects GIFs whose logical screen or frame exceeds
// the pixel cap before allocating frame buffers.
func TestGIFDecoderDimensions(t *testing.T) {
	// 65535×65535 logical screen header.
	hugeScreen := []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00\x3b")
	if _, err := NewGIFDecoder(hugeScreen); err == nil {
		t.Fatal("oversized GIF screen decoded")
	}
	// Small logical screen with a 65535×65535 frame descriptor.
	hugeFrame := []byte{
		'G', 'I', 'F', '8', '9', 'a',
		1, 0, 1, 0, 0, 0, 0,
		0x2c, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0,
		2, 0, 0x3b,
	}
	if _, err := NewGIFDecoder(hugeFrame); err == nil {
		t.Fatal("oversized GIF frame decoded")
	}
}
