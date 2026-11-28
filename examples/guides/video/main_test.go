package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
	"github.com/vivaui/viva/ui/video"
)

// open runs the page in a headless window: the real painter, router and focus
// rules, driven by explicit calls.
func open(t *testing.T, m *model) *app.Driver {
	t.Helper()
	page := ui.NewPage(ui.PageConfig{View: m.page})
	driver, err := app.NewDriver(ui.Configure(page, app.Config{Width: 640, Height: 400}), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		driver.Close()
		m.waves.Close()
		if m.clip != nil {
			m.clip.Close()
		}
	})
	return driver
}

// control is the element a user finds by its name.
func control(t *testing.T, driver *app.Driver, name string) gfx.Hit {
	t.Helper()
	hits := driver.Snapshot().Hits
	for i := len(hits) - 1; i >= 0; i-- {
		hit := hits[i]
		if hit.Label == name && !hit.IsLabel {
			return hit
		}
	}
	t.Fatalf("no control named %q on screen", name)
	return gfx.Hit{}
}

// click presses the middle of the named control.
func click(t *testing.T, driver *app.Driver, name string) {
	t.Helper()
	b := control(t, driver, name).Bounds
	if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
		t.Fatal(err)
	}
}

func shows(driver *app.Driver, want string) bool {
	for _, label := range driver.Snapshot().Labels {
		if strings.Contains(label.Value, want) {
			return true
		}
	}
	return false
}

// The page opens on the clip: its title, and the controls that play it or
// open the clip over it.
func TestThePageOpensOnTheClip(t *testing.T) {
	m, err := newModel()
	if err != nil {
		t.Fatal(err)
	}
	driver := open(t, m)
	if !shows(driver, "Wavelength") {
		t.Fatal("the clip's title is not on screen")
	}
	if !m.waves.Playing() {
		t.Fatal("the background clip starts paused")
	}
	click(t, driver, "Pause")
	if m.waves.Playing() {
		t.Fatal("Pause did not pause the clip")
	}
	if !shows(driver, "Play") {
		t.Fatal("the button does not offer to play the clip again")
	}
}

// Watching opens the color clip over the background, plays it, and closing it
// goes back.
func TestWatchingTheClip(t *testing.T) {
	m, err := newModel()
	if err != nil {
		t.Fatal(err)
	}
	driver := open(t, m)
	click(t, driver, "Watch the clip")
	if m.clip == nil {
		if errors.Is(m.clipErr, video.ErrUnsupported) {
			// No media framework here: the page says so and keeps the clip.
			if !shows(driver, video.ErrUnsupported.Error()) {
				t.Fatal("the page does not say why the clip did not open")
			}
			if !m.waves.Playing() {
				t.Fatal("a platform without video stopped the background")
			}
			return
		}
		t.Fatalf("the clip did not open: %v", m.clipErr)
	}
	if m.waves.Playing() {
		t.Fatal("the background kept playing behind the clip")
	}
	if !shows(driver, "Close") {
		t.Fatal("the clip's controls are not on screen")
	}
	click(t, driver, "Close")
	if m.clip != nil {
		t.Fatal("Close kept the clip open")
	}
	if !m.waves.Playing() || !shows(driver, "Wavelength") {
		t.Fatal("closing the clip did not go back to the background")
	}
}
