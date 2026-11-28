package video

import (
	"time"
)

// defaultFrameDuration is how long a decoder frame with no duration of its
// own shows, as browsers show a GIF frame that names none.
const defaultFrameDuration = 100 * time.Millisecond

// maxDecoderPixels caps the pixel count of a Go-decoded animation (8K×8K) so
// a malformed header cannot exhaust memory before decoding pixels.
const maxDecoderPixels = 8192 * 8192

// Frame is one picture of a decoder.
type Frame struct {
	// Pix is the picture's pixels, premultiplied BGRA, Stride bytes a row,
	// top row first. The player copies it; the decoder may reuse the
	// buffer for its next frame.
	Pix           []byte
	Width, Height int
	Stride        int
	// Duration is how long the frame shows, before the next one. Zero is
	// defaultFrameDuration.
	Duration time.Duration
}

// Decoder is a video decoded in Go, such as an animated GIF or PNG: the
// player draws its frames itself, on every system, without a media
// framework. Its methods are called from one goroutine at a time, never
// from the UI thread.
type Decoder interface {
	// Next returns the frame after the one before, or io.EOF after the
	// last.
	Next() (Frame, error)
	// Rewind makes the next Next return the first frame again.
	Rewind() error
}

// Seeker is a Decoder that can start at a time, so the player can seek
// without decoding every frame. A decoder that is not a Seeker plays from
// its start, and the player ignores Seek.
type Seeker interface {
	Decoder
	// Seek makes the next Next return the frame showing at to, and returns
	// when that frame starts: a frame starts on a multiple of the frames'
	// durations, which may be before to.
	Seek(to time.Duration) (time.Duration, error)
}

// durationReader is what a decoder whose animation has a known length
// implements, for the player's Duration before it reached the end.
type durationReader interface {
	Duration() time.Duration
}
