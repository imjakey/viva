package video

import (
	"sync/atomic"
	"time"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/internal/media"
	"github.com/vivaui/viva/ui"
	"github.com/vivaui/viva/window"
)

// State is where a player is in loading and playing its video.
type State uint8

const (
	// Loading is the video opening, before its first picture.
	Loading State = iota
	// Ready is playing, or paused and able to play.
	Ready
	// Buffering is playing, but waiting for data.
	Buffering
	// Ended is the video played to its end.
	Ended
	// Failed is the video stopped by an error, which Err reports.
	Failed
)

// invalidator wakes the page's window when a picture arrives from a thread
// that is not the UI thread.
type invalidator struct {
	page *ui.Page
	wake window.Invalidator
}

// bitmapIDs names the bitmaps of every player, so two players' pictures do
// not share a name in the renderer's caches.
var bitmapIDs atomic.Uint64

// Player plays one video, and shows it through View. Its methods are called
// on the UI thread; a player is not copied after it has been shown.
type Player struct {
	engine media.Engine
	dec    *decoderPlayback

	inv  atomic.Pointer[invalidator]
	wake func()

	handlers ui.Handlers
	drawFn   func(*ui.DrawContext)
	prepared bool

	fit    Fit
	rate   float64
	closed bool

	// texture is the engine's picture, with the version of its contents.
	texture        gfx.NativeTexture
	textureVersion uint32
	// bitmap is the picture of a decoder, copied from its frames.
	bitmap        gfx.Bitmap
	bitmapVersion uint32
}

func newPlayer() *Player {
	p := &Player{rate: 1}
	p.wake = p.invalidate
	return p
}

func newEnginePlayer(engine media.Engine) *Player {
	p := newPlayer()
	p.engine = engine
	return p
}

// NewDecoder returns a player of a decoder's frames, such as a GIF's. It
// plays on every system, decoded in Go. The player uses the decoder until
// Close.
func NewDecoder(d Decoder) *Player {
	if d == nil {
		panic("video: NewDecoder needs a decoder")
	}
	p := newPlayer()
	p.dec = newDecoderPlayback(d, p.wake)
	return p
}

// Play starts the video, or starts it again after its end.
func (p *Player) Play() {
	if p.closed {
		return
	}
	if p.dec != nil {
		p.dec.play()
	} else if p.engine != nil {
		p.engine.Play()
	}
	p.invalidate()
}

// Pause stops the video where it is.
func (p *Player) Pause() {
	if p.closed {
		return
	}
	if p.dec != nil {
		p.dec.pause()
	} else if p.engine != nil {
		p.engine.Pause()
	}
}

// Playing reports whether the video is playing: not paused, and not ended.
func (p *Player) Playing() bool {
	if p.closed {
		return false
	}
	if p.dec != nil {
		return p.dec.playing
	}
	if p.engine != nil {
		if state := playerState(p.engine); state == Ended || state == Failed {
			return false
		}
		return p.engine.Playing()
	}
	return false
}

// Seek starts the video at d, the nearest frame before it. A decoder that
// cannot seek plays from its start, and Seek does nothing. The position
// reads d until the frame there shows, as with most players.
func (p *Player) Seek(d time.Duration) {
	if p.closed {
		return
	}
	if d < 0 {
		d = 0
	}
	if p.dec != nil {
		p.dec.seek(d)
	} else if p.engine != nil {
		p.engine.Seek(d)
	}
	p.invalidate()
}

// Position is where the video is: the start of the picture shown, while it
// pauses, and the time the next frame is due as it plays.
func (p *Player) Position() time.Duration {
	if p.closed {
		return 0
	}
	if p.dec != nil {
		return p.dec.position()
	}
	if p.engine != nil {
		return p.engine.Position()
	}
	return 0
}

// Duration is the video's length, or 0 while it is not known: before an
// engine's first picture, or a decoder with no frame times of its own.
func (p *Player) Duration() time.Duration {
	if p.closed {
		return 0
	}
	if p.dec != nil {
		return p.dec.duration()
	}
	if p.engine != nil {
		return p.engine.Duration()
	}
	return 0
}

// Size is the picture's size in pixels, or 0, 0 before the first one.
func (p *Player) Size() (w, h int) {
	if p.closed {
		return 0, 0
	}
	if p.dec != nil {
		return p.dec.size()
	}
	if p.engine != nil {
		return p.engine.Size()
	}
	return 0, 0
}

// State is where the player is in loading and playing the video.
func (p *Player) State() State {
	if p.closed {
		return Ended
	}
	if p.dec != nil {
		return p.dec.state
	}
	if p.engine != nil {
		return playerState(p.engine)
	}
	return Loading
}

// Err is the error the video stopped with, or nil.
func (p *Player) Err() error {
	if p.closed {
		return nil
	}
	if p.dec != nil {
		return p.dec.err
	}
	if p.engine != nil {
		_, err := p.engine.State()
		return err
	}
	return nil
}

// SetLoop makes the video play again from its start after its end. It plays
// again on its own, without Play.
func (p *Player) SetLoop(loop bool) {
	if p.closed {
		return
	}
	if p.dec != nil {
		p.dec.loop = loop
	} else if p.engine != nil {
		p.engine.SetLoop(loop)
	}
}

// SetMuted turns the video's sound off or on. A decoder's frames have no
// sound, and SetMuted does nothing.
func (p *Player) SetMuted(muted bool) {
	if p.closed {
		return
	}
	if p.engine != nil {
		p.engine.SetMuted(muted)
	}
}

// SetVolume sets the video's volume, from 0 to 1. A decoder's frames have no
// sound, and SetVolume does nothing.
func (p *Player) SetVolume(volume float64) {
	if p.closed {
		return
	}
	if p.engine != nil {
		p.engine.SetVolume(volume)
	}
}

// SetRate sets how fast the video plays, 1 being its own speed. A rate of
// zero or less keeps a decoder's animation still; an engine's video decides
// what rates it plays at.
func (p *Player) SetRate(rate float64) {
	if p.closed {
		return
	}
	p.rate = rate
	if p.dec != nil {
		p.dec.setRate(rate)
	} else if p.engine != nil {
		p.engine.SetRate(rate)
	}
}

// Fit chooses how the picture fills the view's box, and returns the player.
func (p *Player) Fit(fit Fit) *Player {
	p.fit = fit
	p.invalidate()
	return p
}

// Close stops the video and frees what it made: its decoder, its engine and
// the GPU textures the engine shares. It is safe to call more than once. A
// view of a closed player paints nothing.
func (p *Player) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	p.invalidate()
	if p.dec != nil {
		p.dec.close()
		return nil
	}
	if p.engine != nil {
		return p.engine.Close()
	}
	return nil
}

// prepare makes the player ready to be shown in c: it learns how to wake the
// page's window, and gives an engine the same way.
func (p *Player) prepare(c ui.C) {
	page := c.GetPage()
	if inv := p.inv.Load(); inv == nil || inv.page != page {
		p.inv.Store(&invalidator{page: page, wake: page.GetInvalidator()})
	}
	if !p.prepared {
		p.prepared = true
		p.handlers.Role = gfx.RoleGroup
		p.drawFn = p.draw
	}
	if p.engine != nil {
		// The engine wakes the window from its own threads when its state
		// changes; it is told again each paint, in case the page changed.
		p.engine.SetWake(p.wake)
	}
}

// invalidate asks for one more paint, from any goroutine: a picture arrived,
// or the player's state changed.
func (p *Player) invalidate() {
	inv := p.inv.Load()
	if inv == nil || inv.wake == nil {
		return
	}
	inv.wake.Invalidate()
}

func (p *Player) draw(d *ui.DrawContext) {
	if p.closed {
		return
	}
	if p.dec != nil {
		p.drawDecoder(d)
		return
	}
	p.drawEngine(d)
}

// drawEngine draws the engine's picture, opening its texture on the GPU.
func (p *Player) drawEngine(d *ui.DrawContext) {
	picture, changed, ok := p.engine.Frame()
	if ok && (changed || p.texture.ID != picture.ID || p.texture.Handle != picture.Handle ||
		p.texture.Width != picture.W || p.texture.Height != picture.H) {
		p.textureVersion++
		p.texture = gfx.NativeTexture{ID: picture.ID, Handle: picture.Handle, Width: picture.W, Height: picture.H, Version: p.textureVersion}
	}
	state := playerState(p.engine)
	if p.texture.ID != 0 {
		bounds := fitRect(p.fit, d.Rect, p.texture.Width, p.texture.Height)
		if p.fit == Cover {
			d.Canvas.Clip(d.Rect)
		}
		d.Canvas.DrawTexture(&p.texture, bounds, 1-d.Opacity)
		if p.fit == Cover {
			d.Canvas.Unclip()
		}
	}
	if !d.Canvas.HasClock() {
		return
	}
	if state == Ended || state == Failed {
		return
	}
	if p.engine.Playing() {
		d.Canvas.RequestFrameIn(frameInterval(p.engine.Interval(), p.rate))
		return
	}
	if p.texture.ID == 0 {
		// The first picture may come with no event to wake the window: look
		// again soon, as Media Foundation's samples look every vsync.
		d.Canvas.RequestFrameIn(pollInterval)
	}
}

// drawDecoder draws the decoder's frames, one bitmap per picture.
func (p *Player) drawDecoder(d *ui.DrawContext) {
	now := d.Canvas.Now()
	if p.dec.advance(now) && p.dec.have {
		p.copyFrame(p.dec.cur)
	}
	if p.dec.have && p.bitmap.Width > 0 && p.bitmap.Height > 0 {
		bounds := fitRect(p.fit, d.Rect, p.bitmap.Width, p.bitmap.Height)
		if p.fit == Cover {
			d.Canvas.Clip(d.Rect)
		}
		source := gfx.Rect{Width: float32(p.bitmap.Width), Height: float32(p.bitmap.Height)}
		d.Canvas.DrawBitmapShaded(p.bitmap, source, bounds, 0, 1-d.Opacity)
		if p.fit == Cover {
			d.Canvas.Unclip()
		}
	}
	if !d.Canvas.HasClock() {
		return
	}
	if delay, ok := p.dec.pace(now); ok {
		d.Canvas.RequestFrameIn(delay)
	}
}

// copyFrame copies a decoder frame into the player's bitmap, naming it a new
// picture: the decoder may reuse its buffer for the frame after.
func (p *Player) copyFrame(f Frame) {
	if f.Width <= 0 || f.Height <= 0 || f.Stride < f.Width*4 || len(f.Pix) < f.Stride*f.Height {
		return
	}
	if p.bitmap.ID == 0 {
		p.bitmap.ID = bitmapIDs.Add(1)
	}
	p.bitmap.Pix = append(p.bitmap.Pix[:0], f.Pix[:f.Stride*f.Height]...)
	p.bitmap.Width, p.bitmap.Height, p.bitmap.Stride = f.Width, f.Height, f.Stride
	p.bitmapVersion++
	p.bitmap.Version = p.bitmapVersion
}

// playerState is the engine's state as the player reports it.
func playerState(engine media.Engine) State {
	state, _ := engine.State()
	return State(state)
}

// pollInterval is how soon a view looks again for a picture that comes with
// no event to wake the window.
const pollInterval = 16 * time.Millisecond

// frameInterval is how long a view waits before looking for another picture
// of an engine: the engine's interval, sooner while the rate is faster than
// its own.
func frameInterval(interval time.Duration, rate float64) time.Duration {
	if interval <= 0 {
		interval = time.Second / 60
	}
	if rate > 1 {
		interval = time.Duration(float64(interval) / rate)
	}
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	return interval
}
