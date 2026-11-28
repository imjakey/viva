package video

import (
	"io"
	"time"
)

// decoderAhead is how many frames the decoder decodes before the one shown:
// enough that the next frame is there when its time comes, without decoding
// the whole animation a frame at a time.
const decoderAhead = 2

// decoded is one frame a decoder made, or the error that stopped it.
type decoded struct {
	frame  Frame
	at     time.Duration // when the frame starts
	landed bool          // the first frame after a seek or a rewind
	err    error
	gen    uint64 // the run that made it
}

// decodeRequest makes the decoder start over at a time, or at its start.
type decodeRequest struct {
	gen    uint64
	rewind bool
	to     time.Duration
}

// decoderPlayback plays a Decoder's frames: a goroutine decodes ahead, and
// the UI thread shows the frame due as the canvas clock passes it. The
// goroutine only ever fills the channels; every field below is read and
// written on the UI thread.
type decoderPlayback struct {
	src    Decoder
	seeker Seeker

	cmd  chan decodeRequest
	out  chan decoded
	pool chan []byte
	stop chan struct{}
	done chan struct{}
	wake func()

	total    time.Duration // the animation's length, when known
	end      time.Duration // where its last frame ends
	endKnown bool

	gen     uint64    // the run the player shows
	waiting bool      // a seek's frame has not landed yet
	queue   []decoded // frames decoded but not shown yet
	cur     Frame
	have    bool
	pos     time.Duration // where the shown frame starts

	rate    float64
	playing bool
	loop    bool
	resync  bool // base the play clock at the next advance
	playAt  time.Duration
	playPos time.Duration
	lastNow time.Duration

	state State
	err   error
}

func newDecoderPlayback(src Decoder, wake func()) *decoderPlayback {
	d := &decoderPlayback{src: src, wake: wake, rate: 1, state: Loading}
	if seeker, ok := src.(Seeker); ok {
		d.seeker = seeker
	}
	if reader, ok := src.(durationReader); ok {
		if total := reader.Duration(); total > 0 {
			d.total, d.end, d.endKnown = total, total, true
		}
	}
	d.cmd = make(chan decodeRequest, 1)
	d.out = make(chan decoded, decoderAhead)
	d.pool = make(chan []byte, decoderAhead+2)
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go d.decode()
	d.request(decodeRequest{rewind: true})
	return d
}

// decode is the decoder's goroutine: it takes a request, starts the decoder
// there, and decodes ahead until the animation ends or a newer request comes.
func (d *decoderPlayback) decode() {
	defer close(d.done)
	var (
		next     decodeRequest
		haveNext bool
	)
	for {
		var req decodeRequest
		if haveNext {
			req, haveNext = next, false
		} else {
			select {
			case <-d.stop:
				return
			case req = <-d.cmd:
			}
		}
		at, err := d.start(req)
		if err != nil {
			d.deliver(decoded{at: at, err: err, gen: req.gen})
			continue
		}
		landed := true
	run:
		for {
			frame, err := d.src.Next()
			if err != nil {
				d.deliver(decoded{at: at, err: err, gen: req.gen})
				break run
			}
			duration := frame.Duration
			if duration <= 0 {
				duration = defaultFrameDuration
			}
			// Copy the frame's pixels before the next Next call can reuse
			// the decoder's buffer while this frame is still queued.
			copied := d.cloneFrame(frame)
			select {
			case <-d.stop:
				d.recycle(copied.Pix)
				return
			case d.out <- decoded{frame: copied, at: at, landed: landed, gen: req.gen}:
				d.woken()
			case req = <-d.cmd:
				// A newer run starts: what this one decoded is stale.
				d.recycle(copied.Pix)
				next, haveNext = req, true
				break run
			}
			at += duration
			landed = false
		}
	}
}

// cloneFrame copies frame's pixel bytes into a buffer owned by the queue,
// reusing a pooled slice when one fits.
func (d *decoderPlayback) cloneFrame(frame Frame) Frame {
	if frame.Width <= 0 || frame.Height <= 0 || frame.Stride < frame.Width*4 {
		frame.Pix = nil
		return frame
	}
	n := frame.Stride * frame.Height
	if len(frame.Pix) < n {
		frame.Pix = nil
		return frame
	}
	var buf []byte
	select {
	case buf = <-d.pool:
	default:
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	} else {
		buf = buf[:n]
	}
	copy(buf, frame.Pix[:n])
	frame.Pix = buf
	return frame
}

// recycle returns a pixel buffer to the pool for reuse by the decoder
// goroutine.
func (d *decoderPlayback) recycle(pix []byte) {
	if pix == nil {
		return
	}
	select {
	case d.pool <- pix:
	default:
	}
}

// start makes the decoder return its first frame at the request's time.
func (d *decoderPlayback) start(req decodeRequest) (time.Duration, error) {
	if req.rewind || d.seeker == nil {
		return 0, d.src.Rewind()
	}
	return d.seeker.Seek(req.to)
}

// deliver hands the UI thread a message, unless the player closed first.
func (d *decoderPlayback) deliver(msg decoded) {
	select {
	case <-d.stop:
	case d.out <- msg:
		d.woken()
	}
}

// woken asks for a paint: a frame, or an error, is there.
func (d *decoderPlayback) woken() {
	if d.wake != nil {
		d.wake()
	}
}

// request makes the decoder start at a request, replacing one it has not
// taken yet. It never blocks the UI thread.
func (d *decoderPlayback) request(req decodeRequest) {
	for {
		select {
		case <-d.cmd:
		default:
			select {
			case d.cmd <- req:
			default:
			}
			return
		}
	}
}

// pump takes what the decoder made since the last look.
func (d *decoderPlayback) pump() {
	for {
		select {
		case msg := <-d.out:
			if msg.gen != d.gen {
				d.recycle(msg.frame.Pix)
				continue
			}
			if msg.err != nil {
				d.recycle(msg.frame.Pix)
				d.fail(msg.err, msg.at)
				continue
			}
			d.queue = append(d.queue, msg)
		default:
			return
		}
	}
}

// fail records the end of the animation, or the error that stopped it.
func (d *decoderPlayback) fail(err error, at time.Duration) {
	if err != io.EOF {
		d.err = err
		d.state = Failed
		d.playing = false
		d.waiting = false
		return
	}
	d.end = at
	if at > 0 {
		d.total, d.endKnown = at, true
	}
	if !d.have && len(d.queue) == 0 && !d.endKnown {
		// An animation of no frames.
		d.state = Ended
		d.playing = false
	}
}

// advance shows the frame the clock is in, and ends or loops the animation.
// It reports whether the shown picture changed. now is the canvas time.
func (d *decoderPlayback) advance(now time.Duration) bool {
	d.pump()
	d.lastNow = now
	if d.playing && d.resync {
		d.playAt, d.playPos = now, d.pos
		d.resync = false
	}
	changed := d.show(d.current(now))
	if d.playing && d.resync {
		// A landed frame put the video where a seek or a loop start asked; the
		// clock starts there, not where it was before the frame arrived.
		d.playAt, d.playPos = now, d.pos
		d.resync = false
	}
	if d.playing && d.endKnown && d.current(now) >= d.end {
		d.finish()
	}
	return changed
}

// current is the time the video is at, now: the shown frame's start while it
// pauses, and the playing clock's time as it plays.
func (d *decoderPlayback) current(now time.Duration) time.Duration {
	if !d.playing {
		return d.pos
	}
	current := d.playPos
	if elapsed := now - d.playAt; elapsed > 0 && d.rate > 0 {
		current += time.Duration(float64(elapsed) * d.rate)
	}
	return current
}

// show shows every frame due by current. A landed frame shows whatever the
// clock says: it is where the seek put the video.
func (d *decoderPlayback) show(current time.Duration) bool {
	changed := false
	for len(d.queue) > 0 {
		msg := d.queue[0]
		if !msg.landed && msg.at > current {
			break
		}
		d.queue = d.queue[1:]
		if d.have {
			d.recycle(d.cur.Pix)
		}
		d.cur = msg.frame
		d.have = true
		d.pos = msg.at
		changed = true
		if d.state == Loading {
			d.state = Ready
		}
		if msg.landed {
			d.waiting = false
			if d.playing {
				d.resync = true
			}
		}
	}
	return changed
}

// finish ends the animation, or starts it over when it loops.
func (d *decoderPlayback) finish() {
	if d.loop {
		d.gen++
		d.clearQueue()
		d.waiting = true
		d.pos = 0
		d.resync = true
		d.request(decodeRequest{rewind: true, gen: d.gen})
		return
	}
	d.playing = false
	d.state = Ended
	d.pos = d.end
}

func (d *decoderPlayback) clearQueue() {
	for i := range d.queue {
		d.recycle(d.queue[i].frame.Pix)
		d.queue[i] = decoded{}
	}
	d.queue = d.queue[:0]
}

// pace says when the view should paint again, and whether it should: while
// the decoder owes a frame, and when the next frame is due.
func (d *decoderPlayback) pace(now time.Duration) (time.Duration, bool) {
	if d.waiting || d.state == Loading || d.state == Buffering {
		return pollInterval, true
	}
	if !d.playing || d.state == Ended || d.state == Failed {
		return 0, false
	}
	if len(d.queue) == 0 {
		return pollInterval, true
	}
	delay := d.queue[0].at - d.current(now)
	if d.rate > 0 {
		delay = time.Duration(float64(delay) / d.rate)
	}
	if delay < pollInterval {
		delay = pollInterval
	}
	return delay, true
}

func (d *decoderPlayback) play() {
	if d.state == Ended {
		// Play starts an ended animation over from its start.
		d.gen++
		d.waiting = true
		d.pos = 0
		d.state = Ready
		d.request(decodeRequest{rewind: true, gen: d.gen})
	}
	d.playing = true
	d.resync = true
}

func (d *decoderPlayback) pause() { d.playing = false }

func (d *decoderPlayback) setRate(rate float64) {
	d.rate = rate
	d.resync = true
}

// seek starts the animation at to. A decoder that cannot seek keeps playing.
func (d *decoderPlayback) seek(to time.Duration) {
	if d.seeker == nil {
		return
	}
	d.gen++
	d.clearQueue()
	d.waiting = true
	d.pos = to
	d.resync = d.playing
	d.request(decodeRequest{to: to, gen: d.gen})
}

func (d *decoderPlayback) position() time.Duration {
	if !d.playing || d.resync {
		return d.pos
	}
	current := d.current(d.lastNow)
	if d.endKnown && current > d.end {
		current = d.end
	}
	return current
}

func (d *decoderPlayback) duration() time.Duration {
	if !d.endKnown {
		return 0
	}
	return d.total
}

func (d *decoderPlayback) size() (w, h int) {
	if !d.have {
		return 0, 0
	}
	return d.cur.Width, d.cur.Height
}

func (d *decoderPlayback) close() {
	close(d.stop)
	<-d.done
}
