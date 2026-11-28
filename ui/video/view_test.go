package video

import (
	"math"
	"testing"
	"time"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/internal/media"
)

// fakeEngine is a media engine whose picture, state and clock the test sets.
type fakeEngine struct {
	picture  media.Picture
	ok       bool
	changed  bool
	state    media.State
	err      error
	w, h     int
	duration time.Duration
	position time.Duration
	playing  bool
	interval time.Duration
	wake     func()
	closes   int
	loop     bool
	rate     float64
	volume   float64
	muted    bool
	seeked   time.Duration
}

func (e *fakeEngine) SetWake(wake func())                { e.wake = wake }
func (e *fakeEngine) Frame() (media.Picture, bool, bool) { return e.picture, e.changed, e.ok }
func (e *fakeEngine) Interval() time.Duration            { return e.interval }
func (e *fakeEngine) State() (media.State, error)        { return e.state, e.err }
func (e *fakeEngine) Size() (w, h int)                   { return e.w, e.h }
func (e *fakeEngine) Duration() time.Duration            { return e.duration }
func (e *fakeEngine) Position() time.Duration            { return e.position }
func (e *fakeEngine) Playing() bool                      { return e.playing }
func (e *fakeEngine) Play()                              { e.playing, e.state = true, media.Ready }
func (e *fakeEngine) Pause()                             { e.playing = false }
func (e *fakeEngine) Seek(to time.Duration)              { e.seeked, e.position = to, to }
func (e *fakeEngine) SetLoop(loop bool)                  { e.loop = loop }
func (e *fakeEngine) SetRate(rate float64)               { e.rate = rate }
func (e *fakeEngine) SetVolume(volume float64)           { e.volume = volume }
func (e *fakeEngine) SetMuted(muted bool)                { e.muted = muted }
func (e *fakeEngine) Close() error                       { e.closes++; return nil }

func closeTo(got, want float32) bool { return math.Abs(float64(got-want)) < .001 }

func newEngine(picture media.Picture) *fakeEngine {
	return &fakeEngine{
		picture:  picture,
		ok:       true,
		state:    media.Ready,
		playing:  true,
		interval: 33 * time.Millisecond,
	}
}

// The view draws the engine's picture where it fits, and passes the texture
// and its version to the canvas without a copy.
func TestViewDrawsThePicture(t *testing.T) {
	e := newEngine(media.Picture{Handle: 0x42, ID: 9, W: 640, H: 360})
	p := newEnginePlayer(e)
	defer p.Close()
	h := newHarness(t, p)
	h.draw()

	images := h.canvas.Images()
	if len(images) != 1 {
		t.Fatalf("painted %d images, want 1", len(images))
	}
	painted := images[0]
	if painted.Texture == nil {
		t.Fatal("the image has no texture")
	}
	if *painted.Texture != (gfx.NativeTexture{ID: 9, Handle: 0x42, Width: 640, Height: 360, Version: 1}) {
		t.Fatalf("texture = %+v", *painted.Texture)
	}
	if !closeTo(painted.Bounds.X, 0) || !closeTo(painted.Bounds.Y, 7.5) ||
		!closeTo(painted.Bounds.Width, 400) || !closeTo(painted.Bounds.Height, 225) {
		t.Fatalf("bounds = %+v, want a 400x225 picture centered at y=7.5", painted.Bounds)
	}
	if painted.Fade != 0 {
		t.Fatalf("fade = %g, want 0", painted.Fade)
	}
	if delay, ok := h.canvas.NextFrame(); !ok || delay != e.interval {
		t.Fatalf("the next picture is due in %v, %v; want %v", delay, ok, e.interval)
	}

	// The same picture keeps its version; a new one bumps it.
	h.draw()
	if *h.canvas.Images()[0].Texture != *painted.Texture {
		t.Fatal("redrawing the same picture changed its texture")
	}
	e.picture = media.Picture{Handle: 0x43, ID: 10, W: 640, H: 360}
	h.draw()
	if got := h.canvas.Images()[0].Texture; got.ID != 10 || got.Version != 2 {
		t.Fatalf("after the picture changed texture = %+v, want ID 10 version 2", got)
	}
}

// Cover fills the view's box and clips the picture to it.
func TestViewClipsCover(t *testing.T) {
	e := newEngine(media.Picture{Handle: 0x42, ID: 9, W: 640, H: 360})
	p := newEnginePlayer(e)
	defer p.Close()
	p.Fit(Cover)
	h := newHarness(t, p)
	h.draw()

	images := h.canvas.Images()
	if len(images) != 1 {
		t.Fatalf("painted %d images, want 1", len(images))
	}
	painted := images[0]
	if !closeTo(painted.Bounds.X, -13.333) || !closeTo(painted.Bounds.Y, 0) ||
		!closeTo(painted.Bounds.Width, 426.667) || !closeTo(painted.Bounds.Height, 240) {
		t.Fatalf("bounds = %+v, want a cover 426.667x240 at x=-13.333", painted.Bounds)
	}
	if !painted.HasClip || painted.ClipOf != (gfx.Rect{Width: 400, Height: 240}) {
		t.Fatalf("clip = %+v, %v; want the view's box", painted.ClipOf, painted.HasClip)
	}
}

// Before the first picture the view paints nothing and looks again soon.
func TestViewPollsForTheFirstPicture(t *testing.T) {
	e := &fakeEngine{state: media.Loading, w: 640, h: 360}
	p := newEnginePlayer(e)
	defer p.Close()
	h := newHarness(t, p)
	h.draw()
	if images := h.canvas.Images(); len(images) != 0 {
		t.Fatalf("painted %d images before the first picture", len(images))
	}
	if delay, ok := h.canvas.NextFrame(); !ok || delay != pollInterval {
		t.Fatalf("the next look is in %v, %v; want %v", delay, ok, pollInterval)
	}
}

// The engine wakes the page through the player when its state changes.
func TestViewSetsTheEngineWake(t *testing.T) {
	e := newEngine(media.Picture{Handle: 0x42, ID: 9, W: 640, H: 360})
	p := newEnginePlayer(e)
	defer p.Close()
	h := newHarness(t, p)
	h.draw()
	if e.wake == nil {
		t.Fatal("the view did not give the engine a way to wake it")
	}
	before := h.wake.calls.Load()
	e.wake()
	if got := h.wake.calls.Load() - before; got != 1 {
		t.Fatalf("the engine's wake made %d repaints, want 1", got)
	}
}

// A closed player paints nothing and closes its engine once.
func TestViewClosedPaintsNothing(t *testing.T) {
	e := newEngine(media.Picture{Handle: 0x42, ID: 9, W: 640, H: 360})
	p := newEnginePlayer(e)
	h := newHarness(t, p)
	h.draw()
	if err := p.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close again = %v", err)
	}
	if e.closes != 1 {
		t.Fatalf("the engine was closed %d times, want 1", e.closes)
	}
	h.draw()
	if images := h.canvas.Images(); len(images) != 0 {
		t.Fatalf("a closed player painted %d images", len(images))
	}
	if _, ok := h.canvas.NextFrame(); ok {
		t.Fatal("a closed player asked for another paint")
	}
}

// The player passes its settings to the engine, and reads its state back.
func TestPlayerDrivesTheEngine(t *testing.T) {
	e := newEngine(media.Picture{Handle: 0x42, ID: 9, W: 640, H: 360})
	e.duration = 5 * time.Second
	p := newEnginePlayer(e)
	defer p.Close()
	p.Play()
	p.Seek(2 * time.Second)
	p.SetLoop(true)
	p.SetRate(1.5)
	p.SetVolume(0.25)
	p.SetMuted(true)
	if !e.playing || e.seeked != 2*time.Second || !e.loop || e.rate != 1.5 || e.volume != 0.25 || !e.muted {
		t.Fatalf("the engine got playing %v, seek %v, loop %v, rate %v, volume %v, muted %v",
			e.playing, e.seeked, e.loop, e.rate, e.volume, e.muted)
	}
	if p.Duration() != 5*time.Second || p.Position() != 2*time.Second || !p.Playing() || p.State() != Ready {
		t.Fatalf("the player reports duration %v, position %v, playing %v, state %v",
			p.Duration(), p.Position(), p.Playing(), p.State())
	}
	p.Pause()
	if e.playing || p.Playing() {
		t.Fatal("Pause did not pause the engine")
	}
	e.state = media.Failed
	e.err = media.ErrUnsupported
	if p.State() != Failed || p.Err() != media.ErrUnsupported || p.Playing() {
		t.Fatalf("a failed engine: state %v, err %v, playing %v", p.State(), p.Err(), p.Playing())
	}
}

func TestViewNeedsAPlayer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("View(nil player) did not panic")
		}
	}()
	View(nil, "Video", nil)
}
