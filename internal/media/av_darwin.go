//go:build darwin && cgo && (amd64 || arm64)

package media

// On macOS, AVFoundation plays the video. An AVPlayerItemVideoOutput decodes
// each frame into a BGRA CVPixelBuffer whose memory is an IOSurface; the
// engine keeps the pixel buffers it shows and hands their surfaces to the
// renderer, which wraps them as Metal textures, so a picture reaches the
// window where it was decoded, without a copy through Go.
//
// The engine polls, at the player's frame interval, for a new picture, a
// finished seek and a state change, and wakes the window from that goroutine.

/*
#cgo darwin LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation -framework IOSurface
#include <stdlib.h>
#include "av_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// frameSlots is the number of pictures the engine keeps, leaving the
// renderer time to read one while the engine shows the next.
const frameSlots = 3

// sharedIDs numbers the pictures, for the renderer to tell one from another.
var sharedIDs atomic.Uint64

// avSlot is one picture the engine keeps alive: the pixel buffer AVFoundation
// decoded, and the IOSurface of it the renderer wraps as a texture.
type avSlot struct {
	buffer unsafe.Pointer // CVPixelBufferRef
	handle uintptr        // its IOSurface
	width  int
	height int
	id     uint64
}

// avEngine plays one video with AVFoundation.
type avEngine struct {
	engine *C.viva_media_engine
	stop   chan struct{}
	poke   chan struct{}

	mu      sync.Mutex
	wake    func()
	state   State // Loading until the first picture, then Ready
	failure error
	loop    bool
	rate    float64
	playing bool
	ended   bool
	seeking bool
	seekAt  time.Duration
	shown   int // the slot showing, -1 before the first picture
	width   int
	height  int
	closed  bool

	slots [frameSlots]avSlot
}

// Open opens a video with AVFoundation.
func Open(s Source) (Engine, error) {
	e := &avEngine{shown: -1, state: Loading, rate: 1}
	if err := e.open(s); err != nil {
		e.release()
		return nil, err
	}
	e.stop = make(chan struct{})
	e.poke = make(chan struct{}, 1)
	go e.watch()
	return e, nil
}

func (e *avEngine) open(s Source) error {
	var cErr *C.char
	var engine *C.viva_media_engine
	switch {
	case s.Path != "":
		path := C.CString(s.Path)
		engine = C.viva_media_open_path(path, &cErr)
		C.free(unsafe.Pointer(path))
	case s.URL != "":
		url := C.CString(s.URL)
		engine = C.viva_media_open_url(url, &cErr)
		C.free(unsafe.Pointer(url))
	case len(s.Data) > 0:
		engine = C.viva_media_open_data(unsafe.Pointer(&s.Data[0]), C.size_t(len(s.Data)), &cErr)
	default:
		return errors.New("video: no source")
	}
	if engine == nil {
		if cErr != nil {
			err := fmt.Errorf("video: %s", C.GoString(cErr))
			C.viva_media_free_error(cErr)
			return err
		}
		return errors.New("video: AVFoundation could not open the video")
	}
	e.engine = engine
	return nil
}

// watch polls the player while it is loading, playing or seeking, and parks
// when idle (paused after its first frame, ended without looping, or failed)
// until Play, Seek or SetLoop wakes it.
func (e *avEngine) watch() {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	active := true
	for {
		if active {
			select {
			case <-e.stop:
				return
			case <-e.poke:
			case <-ticker.C:
			}
		} else {
			select {
			case <-e.stop:
				return
			case <-e.poke:
			}
		}
		wake, keepActive := e.observe()
		active = keepActive
		if wake {
			e.wakeWindow()
		}
	}
}

func (e *avEngine) pokeWatcher() {
	if e.poke == nil {
		return
	}
	select {
	case e.poke <- struct{}{}:
	default:
	}
}

// observe reports whether the window should paint again and whether the
// watcher should keep polling.
func (e *avEngine) observe() (changed bool, active bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false, false
	}
	ended := C.viva_media_ended(e.engine) != 0
	if ended && e.loop && e.playing {
		// Loop: play again from the start, as browsers do.
		C.viva_media_clear_ended(e.engine)
		C.viva_media_seek(e.engine, 0)
		C.viva_media_play(e.engine, C.double(e.playRate()))
		e.ended, e.seeking, e.seekAt = false, true, 0
		return true, true
	}
	if ended != e.ended {
		e.ended = ended
		changed = true
	}
	if e.failure == nil {
		e.checkFailure()
	}
	state := e.stateNow()
	if state != e.state {
		e.state = state
		changed = true
	}
	if state == Failed || state == Ended {
		// Terminal state until Play/Seek: wake once on transition, then park.
		return changed, false
	}
	if e.seeking {
		if C.viva_media_seek_landed(e.engine) != 0 {
			changed = true
		}
		return changed, true
	}
	if e.shown < 0 {
		return true, true
	}
	if e.playing {
		if C.viva_media_has_new_frame(e.engine, C.double(e.time())) != 0 {
			changed = true
		}
		return changed, true
	}
	return changed, false
}

// Frame returns the picture showing now, and whether it is newer than the one
// Frame returned before.
func (e *avEngine) Frame() (Picture, bool, bool) {
	changed := e.pull()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.shown < 0 {
		return Picture{}, false, false
	}
	slot := &e.slots[e.shown]
	return Picture{Handle: slot.handle, ID: slot.id, W: slot.width, H: slot.height}, changed, true
}

// pull takes the picture due into the next slot. It forces the picture when
// the output does not call it new yet: the first picture after Open, and the
// frame a seek lands on once AVFoundation reports the seek complete.
func (e *avEngine) pull() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	if e.seeking && C.viva_media_seek_landed(e.engine) == 0 {
		// Wait for AVFoundation's seek completionHandler before pulling a
		// frame so we never mistake a pre-seek buffered frame for the target.
		return false
	}
	force := C.int(0)
	if e.seeking || e.shown < 0 {
		force = 1
	}
	var w, h C.int
	var surface unsafe.Pointer
	buffer := C.viva_media_copy_frame(e.engine, C.double(e.time()), force, &w, &h, &surface)
	if buffer == nil || w <= 0 || h <= 0 {
		if buffer != nil {
			C.viva_media_release_frame(buffer)
		}
		return false
	}
	next := (e.shown + 1) % frameSlots
	slot := &e.slots[next]
	if slot.buffer != nil {
		C.viva_media_release_frame(slot.buffer)
	}
	if slot.id == 0 {
		slot.id = sharedIDs.Add(1)
	}
	slot.buffer = buffer
	slot.handle = uintptr(surface)
	slot.width, slot.height = int(w), int(h)
	e.shown = next
	e.width, e.height = int(w), int(h)
	e.seeking = false
	e.seekAt = 0
	if e.state == Loading {
		e.state = Ready
	}
	return true
}

// time is the item time frames are read at: the seek target while a seek is
// landing, the player's clock otherwise.
func (e *avEngine) time() float64 {
	if e.seeking {
		return e.seekAt.Seconds()
	}
	return C.viva_media_position(e.engine)
}

// playRate is the rate Play plays at.
func (e *avEngine) playRate() float64 {
	if e.rate <= 0 {
		return 1
	}
	return e.rate
}

// checkFailure records the item's failure, if it failed.
func (e *avEngine) checkFailure() {
	if e.engine == nil || C.viva_media_status(e.engine) != 2 {
		return
	}
	if message := C.viva_media_error(e.engine); message != nil {
		e.failure = fmt.Errorf("video: %s", C.GoString(message))
		C.viva_media_free_error(message)
		return
	}
	e.failure = errors.New("video: the video failed to play")
}

// stateNow is where the engine is, in the report the player uses.
func (e *avEngine) stateNow() State {
	if e.failure != nil {
		return Failed
	}
	if e.ended {
		return Ended
	}
	if e.shown < 0 {
		return Loading
	}
	if e.playing && C.viva_media_waiting(e.engine) != 0 {
		return Buffering
	}
	return Ready
}

func (e *avEngine) SetWake(wake func()) {
	e.mu.Lock()
	e.wake = wake
	e.mu.Unlock()
}

func (e *avEngine) wakeWindow() {
	e.mu.Lock()
	wake := e.wake
	e.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (e *avEngine) Interval() time.Duration { return time.Second / 60 }

func (e *avEngine) State() (State, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Ended, nil
	}
	if e.failure == nil {
		e.checkFailure()
	}
	if e.failure != nil {
		return Failed, e.failure
	}
	if e.ended {
		return Ended, nil
	}
	if e.shown < 0 {
		return Loading, nil
	}
	if e.playing && C.viva_media_waiting(e.engine) != 0 {
		return Buffering, nil
	}
	return Ready, nil
}

func (e *avEngine) Size() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.width, e.height
}

func (e *avEngine) Duration() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	seconds := C.viva_media_duration(e.engine)
	return time.Duration(seconds * float64(time.Second))
}

func (e *avEngine) Position() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seeking {
		return e.seekAt
	}
	return time.Duration(C.viva_media_position(e.engine) * float64(time.Second))
}

func (e *avEngine) Playing() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.closed && e.engine != nil && e.playing && !e.ended
}

func (e *avEngine) Play() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if e.ended {
		// Play after the end starts over, as the decoder player does.
		C.viva_media_clear_ended(e.engine)
		C.viva_media_seek(e.engine, 0)
		e.ended, e.seeking, e.seekAt = false, true, 0
	}
	e.playing = true
	C.viva_media_play(e.engine, C.double(e.playRate()))
	e.mu.Unlock()
	e.pokeWatcher()
	e.wakeWindow()
}

func (e *avEngine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.playing = false
	C.viva_media_pause(e.engine)
}

func (e *avEngine) Seek(d time.Duration) {
	if d < 0 {
		d = 0
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.ended = false
	e.seeking = true
	e.seekAt = d
	C.viva_media_clear_ended(e.engine)
	C.viva_media_seek(e.engine, C.double(d.Seconds()))
	e.mu.Unlock()
	e.pokeWatcher()
	e.wakeWindow()
}

func (e *avEngine) SetLoop(loop bool) {
	e.mu.Lock()
	e.loop = loop
	e.mu.Unlock()
	if loop {
		e.pokeWatcher()
	}
}

func (e *avEngine) SetRate(rate float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.rate = rate
	if e.playing {
		C.viva_media_set_rate(e.engine, C.double(e.playRate()))
	}
}

func (e *avEngine) SetVolume(volume float64) {
	if volume < 0 {
		volume = 0
	}
	if volume > 1 {
		volume = 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	C.viva_media_set_volume(e.engine, C.double(volume))
}

func (e *avEngine) SetMuted(muted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	value := C.int(0)
	if muted {
		value = 1
	}
	C.viva_media_set_muted(e.engine, value)
}

// Close stops the player and frees what it made.
func (e *avEngine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.wake = nil
	stop := e.stop
	e.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	e.mu.Lock()
	e.release()
	e.mu.Unlock()
	return nil
}

// release frees the pictures and the player. It is called with the engine
// closed, so no other call can reach the player while it goes.
func (e *avEngine) release() {
	for i := range e.slots {
		e.freeSlot(&e.slots[i])
	}
	if e.engine != nil {
		C.viva_media_close(e.engine)
		e.engine = nil
	}
}

func (e *avEngine) freeSlot(slot *avSlot) {
	if slot.buffer != nil {
		C.viva_media_release_frame(slot.buffer)
	}
	*slot = avSlot{}
}
