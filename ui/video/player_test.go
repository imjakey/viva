package video

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// fakeWake counts the repaints a player asked for, from any goroutine.
type fakeWake struct{ calls atomic.Int64 }

func (w *fakeWake) Invalidate() { w.calls.Add(1) }

// harness is a page that shows one player, with a clock the test moves.
type harness struct {
	player *Player
	page   *ui.Page
	canvas *gfx.Canvas
	wake   *fakeWake
	now    time.Duration
}

func newHarness(t *testing.T, p *Player) *harness {
	t.Helper()
	h := &harness{player: p, canvas: new(gfx.Canvas), wake: new(fakeWake)}
	h.canvas.SetClock(func() time.Duration { return h.now })
	theme := gfx.LightTheme()
	h.page = ui.NewPage(ui.PageConfig{View: h.viewPage, Theme: &theme, Invalidator: h.wake})
	return h
}

func (h *harness) viewPage(c ui.C) {
	View(c, "Video", h.player).FillWidth().Height(240)
}

func (h *harness) draw() {
	h.canvas.Reset()
	h.canvas.BeginFrame()
	h.page.Draw(h.canvas, gfx.Rect{Width: 400, Height: 300}, h.viewPage)
	h.canvas.EndFrame()
}

// settle draws until the player shows a picture: the decoder runs on its own
// goroutine, so its first frame arrives a moment after the player opens.
func (h *harness) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.draw()
		if h.player.dec.have {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the decoder made no frame")
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeDecoder plays a list of frames, and seeks by their durations.
type fakeDecoder struct {
	frames []Frame
	next   int
	rewind int
	seeks  int
}

func (d *fakeDecoder) Next() (Frame, error) {
	if d.next >= len(d.frames) {
		return Frame{}, io.EOF
	}
	frame := d.frames[d.next]
	d.next++
	return frame, nil
}

func (d *fakeDecoder) Rewind() error {
	d.next = 0
	d.rewind++
	return nil
}

func (d *fakeDecoder) Seek(to time.Duration) (time.Duration, error) {
	d.seeks++
	var at time.Duration
	index := 0
	for index+1 < len(d.frames) && at+frameDuration(d.frames[index]) <= to {
		at += frameDuration(d.frames[index])
		index++
	}
	d.next = index
	return at, nil
}

func frameDuration(frame Frame) time.Duration {
	if frame.Duration <= 0 {
		return defaultFrameDuration
	}
	return frame.Duration
}

var (
	red   = Frame{Pix: []byte{0, 0, 255, 255}, Width: 1, Height: 1, Stride: 4, Duration: 100 * time.Millisecond}
	green = Frame{Pix: []byte{0, 255, 0, 255}, Width: 1, Height: 1, Stride: 4, Duration: 100 * time.Millisecond}
	blue  = Frame{Pix: []byte{255, 0, 0, 255}, Width: 1, Height: 1, Stride: 4, Duration: 100 * time.Millisecond}
)

func newFakePlayer(frames ...Frame) (*Player, *fakeDecoder) {
	d := &fakeDecoder{frames: frames}
	return NewDecoder(d), d
}

func paintedPixel(t *testing.T, h *harness) []byte {
	t.Helper()
	images := h.canvas.Images()
	if len(images) != 1 {
		t.Fatalf("painted %d images, want 1", len(images))
	}
	if len(images[0].Bitmap.Pix) < 4 {
		t.Fatal("the painted bitmap has no pixels")
	}
	return images[0].Bitmap.Pix[:4]
}

func wantPixel(t *testing.T, h *harness, want []byte, what string) {
	t.Helper()
	got := paintedPixel(t, h)
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("%s painted %v, want %v", what, got, want)
	}
}

// drawUntil draws at the harness's time until done says the state arrived.
func (h *harness) drawUntil(t *testing.T, done func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.draw()
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Frames show as the canvas clock passes their times, not all at once: the
// view paints the frame the video is in.
func TestDecoderShowsFramesOnTime(t *testing.T) {
	p, _ := newFakePlayer(red, green, blue)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)
	wantPixel(t, h, red.Pix, "the first frame")
	if p.State() != Ready || !p.Playing() {
		t.Fatalf("state %v, playing %v; want a playing video", p.State(), p.Playing())
	}

	h.now = 50 * time.Millisecond
	h.draw()
	wantPixel(t, h, red.Pix, "mid-frame")

	h.now = 100 * time.Millisecond
	h.draw()
	wantPixel(t, h, green.Pix, "the second frame")

	h.now = 200 * time.Millisecond
	h.draw()
	wantPixel(t, h, blue.Pix, "the third frame")
}

// The view asks for the next paint when the next frame is due.
func TestDecoderPacesItsFrames(t *testing.T) {
	p, _ := newFakePlayer(red, green, blue)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)
	h.draw()
	if delay, ok := h.canvas.NextFrame(); !ok || delay != 100*time.Millisecond {
		t.Fatalf("the next frame is in %v, %v; want 100ms", delay, ok)
	}

	p.SetRate(2)
	h.draw()
	if delay, ok := h.canvas.NextFrame(); !ok || delay != 50*time.Millisecond {
		t.Fatalf("at twice the rate the next frame is in %v, %v; want 50ms", delay, ok)
	}
}

// Pausing keeps the picture and the clock where they were; the clock starts
// again from there.
func TestDecoderPausesAndResumes(t *testing.T) {
	p, _ := newFakePlayer(red, green, blue)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)

	h.now = 150 * time.Millisecond
	h.draw()
	wantPixel(t, h, green.Pix, "the second frame")

	p.Pause()
	h.now = 500 * time.Millisecond
	h.draw()
	wantPixel(t, h, green.Pix, "a paused frame")
	if p.Position() != 100*time.Millisecond {
		t.Fatalf("paused at %v, want the shown frame's start, 100ms", p.Position())
	}
	if p.Playing() {
		t.Fatal("a paused video reports playing")
	}

	p.Play()
	h.now = 600 * time.Millisecond
	h.draw()
	wantPixel(t, h, green.Pix, "the frame just after resuming")
	if p.Position() != 100*time.Millisecond {
		t.Fatalf("position just after resuming = %v, want 100ms", p.Position())
	}
	h.now = 700 * time.Millisecond
	h.draw()
	wantPixel(t, h, blue.Pix, "the frame 100ms after resuming")
	if p.Position() != 200*time.Millisecond {
		t.Fatalf("100ms after resuming at 100ms, position = %v, want 200ms", p.Position())
	}
}

// A seek shows the nearest frame at or before the time, paused or playing.
func TestDecoderSeeks(t *testing.T) {
	p, d := newFakePlayer(red, green, blue)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)

	p.Seek(250 * time.Millisecond)
	h.now = 250 * time.Millisecond
	h.drawUntil(t, func() bool { return !p.dec.waiting }, "the seek's frame never landed")
	if d.seeks != 1 {
		t.Fatalf("the decoder was asked to seek %d times, want 1", d.seeks)
	}
	wantPixel(t, h, blue.Pix, "the frame at 250ms")
	if p.Position() != 200*time.Millisecond {
		t.Fatalf("position after seeking to 250ms = %v, want 200ms", p.Position())
	}
}

// A looping video starts again by itself when it ends.
func TestDecoderLoops(t *testing.T) {
	p, d := newFakePlayer(red, green)
	defer p.Close()
	p.SetLoop(true)
	h := newHarness(t, p)
	p.Play()
	h.settle(t)

	h.now = 100 * time.Millisecond
	h.draw()
	wantPixel(t, h, green.Pix, "the second frame")

	h.now = 200 * time.Millisecond
	h.draw()
	if p.State() != Ready || !p.Playing() {
		t.Fatalf("at the end of a looping video: state %v, playing %v", p.State(), p.Playing())
	}
	h.drawUntil(t, func() bool {
		got := paintedPixel(t, h)
		return got[0] == red.Pix[0] && got[1] == red.Pix[1] && got[2] == red.Pix[2]
	}, "the looping video did not start again")
	if p.Position() > 100*time.Millisecond {
		t.Fatalf("position after the loop started again = %v, want near 0", p.Position())
	}
	if d.rewind < 2 {
		t.Fatalf("the decoder was rewound %d times, want a rewind for the loop", d.rewind)
	}
}

// A video that does not loop ends at its end and stays there.
func TestDecoderEnds(t *testing.T) {
	p, _ := newFakePlayer(red, green)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)

	h.now = 200 * time.Millisecond
	h.drawUntil(t, func() bool { return p.State() == Ended }, "the video never ended")
	wantPixel(t, h, green.Pix, "the last frame")
	if p.Playing() {
		t.Fatal("an ended video reports playing")
	}
	if p.Duration() != 200*time.Millisecond || p.Position() != 200*time.Millisecond {
		t.Fatalf("duration %v, position %v; want 200ms", p.Duration(), p.Position())
	}
	if _, ok := h.canvas.NextFrame(); ok {
		t.Fatal("an ended video asked for another paint")
	}

	// Play starts it again from its start.
	p.Play()
	h.drawUntil(t, func() bool {
		got := paintedPixel(t, h)
		return got[1] == red.Pix[1] && got[2] == red.Pix[2]
	}, "Play after the end did not start the video again")
	if p.Position() > 100*time.Millisecond {
		t.Fatalf("position after playing again = %v, want near 0", p.Position())
	}
}

// A faster rate covers more of the video in the same canvas time.
func TestDecoderRateScalesTheClock(t *testing.T) {
	p, _ := newFakePlayer(red, green, blue)
	defer p.Close()
	h := newHarness(t, p)
	p.SetRate(2)
	p.Play()
	h.settle(t)

	h.now = 100 * time.Millisecond
	h.draw()
	wantPixel(t, h, blue.Pix, "200ms of video after 100ms at twice the rate")
	if p.Position() != 200*time.Millisecond {
		t.Fatalf("position = %v, want 200ms", p.Position())
	}
}

// Close stops the decoder and the view paints nothing; it may be called
// twice, and a closed player ignores the other methods.
func TestDecoderClose(t *testing.T) {
	p, _ := newFakePlayer(red, green)
	h := newHarness(t, p)
	p.Play()
	h.settle(t)
	if err := p.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close again = %v", err)
	}
	p.Play()
	p.Pause()
	p.Seek(time.Second)
	if p.Playing() || p.Position() != 0 || p.Duration() != 0 || p.State() != Ended {
		t.Fatalf("a closed player still reports: playing %v, position %v, duration %v, state %v",
			p.Playing(), p.Position(), p.Duration(), p.State())
	}
	h.draw()
	if images := h.canvas.Images(); len(images) != 0 {
		t.Fatalf("a closed player painted %d images", len(images))
	}
	if _, ok := h.canvas.NextFrame(); ok {
		t.Fatal("a closed player asked for another paint")
	}
}

// A decoder frame's pixels are copied: the decoder may reuse its buffer.
func TestDecoderCopiesFrames(t *testing.T) {
	p, d := newFakePlayer(red)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)
	h.drawUntil(t, func() bool { return p.dec.endKnown }, "the decoder never reached its end")

	frame := d.frames[0]
	frame.Pix = []byte{1, 2, 3, 4}
	d.frames[0] = frame
	h.draw()
	wantPixel(t, h, red.Pix, "the player's copy of the frame")
}

// reusingDecoder writes every frame into the same backing buffer, as the GIF
// and APNG decoders do.
type reusingDecoder struct {
	buf    []byte
	colors [][4]byte
	next   int
}

func (d *reusingDecoder) Next() (Frame, error) {
	if d.next >= len(d.colors) {
		return Frame{}, io.EOF
	}
	c := d.colors[d.next]
	d.next++
	for i := 0; i < len(d.buf); i += 4 {
		d.buf[i], d.buf[i+1], d.buf[i+2], d.buf[i+3] = c[0], c[1], c[2], c[3]
	}
	return Frame{
		Pix:      d.buf,
		Width:    4,
		Height:   4,
		Stride:   16,
		Duration: 100 * time.Millisecond,
	}, nil
}

func (d *reusingDecoder) Rewind() error {
	d.next = 0
	return nil
}

// A decoder that reuses one pixel buffer across frames must not race with
// the UI thread or overwrite frames still waiting in the queue.
func TestDecoderReusesSharedBufferSafely(t *testing.T) {
	colors := [][4]byte{
		{0, 0, 255, 255},
		{0, 255, 0, 255},
		{255, 0, 0, 255},
		{255, 255, 0, 255},
		{0, 255, 255, 255},
	}
	dec := &reusingDecoder{buf: make([]byte, 4*4*4), colors: colors}
	p := NewDecoder(dec)
	defer p.Close()
	h := newHarness(t, p)
	p.Play()
	h.settle(t)
	// Give the decoder goroutine time to decode ahead and overwrite dec.buf.
	time.Sleep(10 * time.Millisecond)

	for i, want := range colors {
		h.now = time.Duration(i) * 100 * time.Millisecond
		h.draw()
		wantPixel(t, h, want[:], "frame from reusing decoder")
	}
}

func TestNewDecoderNeedsADecoder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewDecoder(nil) did not panic")
		}
	}()
	NewDecoder(nil)
}
