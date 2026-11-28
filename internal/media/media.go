// Package media is the contract between a video player and a system media
// engine: an engine decodes a video and hands out its pictures, as GPU
// textures the renderer shares, and knows nothing of gfx or of a window.
package media

import (
	"errors"
	"time"
)

// ErrUnsupported reports a system without a media framework that plays
// video: a Windows without Media Foundation, or an OS this release has no
// engine for, such as a macOS built without cgo.
var ErrUnsupported = errors.New("video: the system has no media framework that plays video")

// Source is what an engine opens: a URL, or a video in memory.
type Source struct {
	// URL is a file:, http: or https: URL.
	URL string
	// Path is the local file URL names, which the engine opens as a path.
	Path string
	// Data is the video in memory; URL is empty then. Engines copy the bytes
	// when opening (into a memory stream on Windows, or a temporary file on
	// macOS).
	Data []byte
}

// Picture is a frame where the engine left it: a GPU texture shared by the
// platform. The renderer opens it; the engine keeps it alive until the ring
// slot it is in is used again or the engine closes.
type Picture struct {
	Handle uintptr // the shared texture: an NT handle, or an IOSurface on macOS
	ID     uint64  // stable while the texture lives
	W, H   int
}

// State is where an engine is in loading and playing its video.
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
	// Failed is the video stopped by an error, which Err reads.
	Failed
)

// Engine plays one video with the system's media framework. Its methods are
// called on the UI thread, except SetWake's callback, which the engine calls
// from a thread of its own.
type Engine interface {
	// SetWake gives the engine what to call, from any goroutine, when its
	// state changed and the window should paint again. A nil wake stops
	// the calls.
	SetWake(wake func())
	// Frame returns the picture the engine has now, whether it is newer
	// than the one Frame returned before, and whether there is one.
	Frame() (picture Picture, changed bool, ok bool)
	// Interval is how often the engine shows a picture, 60 a second.
	Interval() time.Duration
	State() (State, error)
	Size() (w, h int)
	Duration() time.Duration
	Position() time.Duration
	Playing() bool
	Play()
	Pause()
	Seek(time.Duration)
	SetLoop(loop bool)
	SetRate(rate float64)
	SetVolume(volume float64)
	SetMuted(muted bool)
	// Close stops the engine and frees what it made. It is safe to call
	// more than once.
	Close() error
}
