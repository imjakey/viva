//go:build windows && (amd64 || arm64)

package media

import (
	"errors"
	"testing"
	"time"
	"unsafe"
)

func TestEnginePlaysColorVideo(t *testing.T) {
	engine, err := Open(Source{Path: "testdata/colors.mp4"})
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			t.Skipf("no media framework: %v", err)
		}
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
	// Methods called after Close must be safe no-ops rather than dereferencing a zero COM pointer.
	engine.Play()
	engine.Pause()
	engine.Seek(time.Second)
	engine.SetLoop(true)
	engine.SetRate(1.5)
	engine.SetVolume(0.5)
	engine.SetMuted(true)
	if engine.Playing() || engine.Duration() != 0 || engine.Position() != 0 {
		t.Fatal("closed engine still reported playback activity")
	}
	if _, _, ok := engine.Frame(); ok {
		t.Fatal("closed engine returned a frame")
	}
	if state, err := engine.State(); state != Ended || err != nil {
		t.Fatalf("closed engine State = (%v, %v), want (Ended, nil)", state, err)
	}
}

// TestNotifyRefCountAndDetach verifies that the COM notify object tracks
// references atomically, ignores events once detached, and stays valid until
// the last COM reference is released.
func TestNotifyRefCountAndDetach(t *testing.T) {
	if err := loadMediaFoundation(); err != nil {
		t.Skipf("no media framework: %v", err)
	}
	e := &mediaEngine{shown: -1}
	notify := newNotify(e)
	if notify == 0 {
		t.Fatal("newNotify returned 0")
	}
	// QueryInterface for IMFMediaEngineNotify must succeed and AddRef (refs: 1 -> 2).
	var out uintptr
	if hr := invoke(notify, comQueryInterface, uintptr(unsafe.Pointer(&iidNotify)), uintptr(unsafe.Pointer(&out))); hresultFailed(hr) || out != notify {
		t.Fatalf("QueryInterface = %#x, out = %#x", hr, out)
	}
	// Simulate engine teardown detaching the engine and releasing Go's ref (refs: 2 -> 1).
	liveEngines.Lock()
	delete(liveEngines.byNotify, notify)
	liveEngines.Unlock()
	if rem := invoke(notify, comRelease); rem != 1 {
		t.Fatalf("remaining refs after Go release = %d, want 1", rem)
	}
	// A late Media Foundation callback after detach must not touch e or crash.
	invoke(notify, 3, eventError, 1, 2)
	if state, err := e.State(); state != Loading || err != nil {
		t.Fatalf("detached engine state changed to (%v, %v)", state, err)
	}
	// Final release from MF frees the object (refs: 1 -> 0).
	if rem := invoke(notify, comRelease); rem != 0 {
		t.Fatalf("remaining refs after final release = %d, want 0", rem)
	}
}

// TestSharedTextureErrorReporting verifies that renderer open failures surface
// on the owning engine and retired texture IDs are tracked.
func TestSharedTextureErrorReporting(t *testing.T) {
	e := &mediaEngine{shown: -1}
	woken := false
	e.SetWake(func() { woken = true })
	id := sharedIDs.Add(1)
	registerSharedTexture(id, e)
	ReportSharedTextureError(id, errors.New("adapter mismatch"))
	state, err := e.State()
	if state != Failed || err == nil {
		t.Fatalf("State = (%v, %v), want (Failed, error)", state, err)
	}
	if !woken {
		t.Fatal("ReportSharedTextureError did not wake the window")
	}
	retireSharedTexture(id)
	if !IsSharedTextureRetired(id) {
		t.Fatal("retired texture ID was not marked retired")
	}
}
