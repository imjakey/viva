// Package video plays videos, as pictures: a system media framework decodes
// them where the system has one, and Go decodes animated GIFs and PNGs on
// every system. The application gives the player its own controls; View shows
// the picture, fit into the box the page gives it.
//
//	//go:embed trailer.mp4
//	var trailer []byte
//
//	m.video, err = video.OpenData(trailer)
//	...
//	m.video.Fit(video.Cover)
//	m.video.SetLoop(true)
//	m.video.Play()
//	video.View(c, "Trailer", m.video).Fill().Height(240)
//
// A player opens paused, and loads in the background: its Size is 0×0 and its
// State Loading until its first picture, and Play starts it. The system's
// decoders play what the system plays: H.264 in MP4 and MOV on Windows, where
// Media Foundation has them, and on macOS, where AVFoundation has them.
// Elsewhere Open reports ErrUnsupported, and the application may show a GIF in
// its place. A player made with NewDecoder plays on every system.
//
// A player's methods are called on the UI thread. Keep one in the model, call
// Close when done, and declare its View in the page's own paint, not inside a
// Memo.
package video

import (
	"errors"
	"net/url"
	"path/filepath"

	"github.com/vivaui/viva/internal/media"
)

// ErrUnsupported reports a system without a media framework that plays
// video: an OS this release has no engine for, or a Windows without Media
// Foundation. An application may show a GIF in its place, with NewDecoder.
var ErrUnsupported = media.ErrUnsupported

// Open opens a video file, playing once Play is called.
func Open(path string) (*Player, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return open(media.Source{URL: fileURL(abs), Path: abs})
}

// OpenURL opens a video at a URL: a file:, http: or https: URL. It plays once
// Play is called.
func OpenURL(url string) (*Player, error) {
	return open(media.Source{URL: url})
}

// OpenData opens a video in memory, as one embedded in the application,
// without the application writing it to a file. Windows copies the bytes into
// a memory stream (SHCreateMemStream); macOS's AVFoundation reads a file, so
// the engine writes the bytes to a temporary file, which it removes when the
// player closes. The caller may reuse or modify data after OpenData returns.
func OpenData(data []byte) (*Player, error) {
	if len(data) == 0 {
		return nil, errors.New("video: no data")
	}
	return open(media.Source{Data: data})
}

func open(s media.Source) (*Player, error) {
	engine, err := media.Open(s)
	if err != nil {
		return nil, err
	}
	return newEnginePlayer(engine), nil
}

// fileURL is the file: URL of an absolute path, its characters escaped as a
// URL needs them.
func fileURL(abs string) string {
	p := filepath.ToSlash(abs)
	if len(p) == 0 || p[0] != '/' {
		p = "/" + p // C:/… on Windows
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}
