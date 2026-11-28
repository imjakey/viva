//go:build darwin && cgo && (amd64 || arm64)

package media

import (
	"testing"
	"time"
)

func TestEnginePlaysColorVideo(t *testing.T) {
	engine, err := Open(Source{Path: "testdata/colors.mp4"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()
	woken := make(chan struct{}, 1)
	engine.SetWake(func() {
		select {
		case woken <- struct{}{}:
		default:
		}
	})
	engine.Play()
	var picture Picture
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		if picture, _, ok = engine.Frame(); ok {
			break
		}
		select {
		case <-woken:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if picture.Handle == 0 {
		t.Fatal("no picture in 10s")
	}
	if picture.ID == 0 || picture.W != 640 || picture.H != 360 {
		t.Fatalf("picture = %+v, want 640x360", picture)
	}
	if w, h := engine.Size(); w != picture.W || h != picture.H {
		t.Errorf("Size = %dx%d, picture %dx%d", w, h, picture.W, picture.H)
	}
	if d := engine.Duration(); d < 5*time.Second || d > 6*time.Second {
		t.Errorf("Duration = %v, want 5s", d)
	}
	engine.Pause()
	if engine.Playing() {
		t.Error("a paused engine reports playing")
	}
	engine.Seek(0)
	engine.Play()
	if state, err := engine.State(); err != nil || state == Failed {
		t.Fatalf("after seek: %v, %v", state, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
