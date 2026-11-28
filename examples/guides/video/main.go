// Video plays video in a Viva window, as a launcher's store page does: a
// short color-wave GIF loops behind the window's content, and a five-second
// color clip plays over it, with controls to pause it, seek and close it.
//
//	go run ./examples/guides/video
//
// The GIF decodes in Go, on every system: package video.NewGIFDecoder's
// frames decode on a goroutine a few ahead, and each reaches the screen as it
// comes due, so the window draws one frame for each of the clip's and none
// between.
//
// The color clip is a 640×360 MP4 embedded in the application, made for this
// example: five seconds of full-frame color changes at 30 frames a second. It
// plays with the system's media framework, Media Foundation on Windows and
// AVFoundation on macOS, and its pictures reach the window's renderer where
// the decoder left them, on the GPU. Elsewhere OpenData reports
// ErrUnsupported: the example says so and keeps the GIF playing.
package main

import (
	_ "embed"
	"fmt"
	"log"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/ui"
	"github.com/vivaui/viva/ui/video"
)

var (
	//go:embed waves.gif
	wavesGIF []byte
	//go:embed colors.mp4
	clipMP4 []byte
)

// model is the state the window shows.
type model struct {
	waves *video.Player
	// clip is the color clip's player once opened, and clipErr what stopped it
	// from opening, as a system without a media framework.
	clip    *video.Player
	clipErr error
	seek    ui.SliderState
}

func newModel() (*model, error) {
	d, err := video.NewGIFDecoder(wavesGIF)
	if err != nil {
		return nil, err
	}
	waves := video.NewDecoder(d)
	waves.Fit(video.Cover)
	waves.SetLoop(true)
	waves.Play()
	return &model{waves: waves}, nil
}

// openClip opens the color clip, which plays once its view shows it. The
// background holds still while the clip plays.
func (m *model) openClip() {
	m.clip, m.clipErr = video.OpenData(clipMP4)
	if m.clip != nil {
		m.clip.Fit(video.Contain)
		m.waves.Pause()
	}
}

// closeClip stops the clip and goes back to the background.
func (m *model) closeClip() {
	m.clip.Close()
	m.clip = nil
	m.seek.Value = 0
	m.waves.Play()
}

// page builds the interface from the state, on the main thread, whenever the
// window needs a frame.
func (m *model) page(c ui.C) {
	white := ui.RGB(255, 255, 255)
	ui.Stack(c).Fill().Children(func() {
		video.View(c, "Waves", m.waves).Fill()
		// Over the clip, which takes the window.
		ui.Column(c).Absolute().Inset(0).Center().Gap(16).Children(func() {
			if m.clip != nil {
				m.clipView(c)
				return
			}
			ui.Text(c, "Wavelength").FontSize(44).Bold().Color(white)
			ui.Row(c).Gap(8).Children(func() {
				label := "Pause"
				if !m.waves.Playing() {
					label = "Play"
				}
				if ui.Button(c, label).Width(96).Clicked() {
					if m.waves.Playing() {
						m.waves.Pause()
					} else {
						m.waves.Play()
					}
				}
				if ui.Button(c, "Watch the clip").Clicked() {
					m.openClip()
				}
			})
			if m.clipErr != nil {
				ui.Text(c, m.clipErr.Error()).Color(white)
			}
		})
	})
}

// clipView shows the color clip and its controls.
func (m *model) clipView(c ui.C) {
	p := m.clip
	white := ui.RGB(255, 255, 255)
	ui.Column(c).Width(480).Gap(8).Pad(12).Background(ui.RGBA(0, 0, 0, 0.78)).Radius(12).Children(func() {
		ui.Stack(c).Width(456).Height(256).Children(func() {
			video.View(c, "Clip", p).Fill()
			if p.Err() != nil {
				ui.Text(c, p.Err().Error()).Color(white)
			}
		})
		ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
			label := "Pause"
			if !p.Playing() {
				label = "Play"
			}
			if ui.Button(c, label).Width(72).Clicked() {
				if p.Playing() {
					p.Pause()
				} else {
					p.Play()
				}
			}
			duration := float32(p.Duration().Seconds())
			if duration <= 0 {
				duration = 0.001
			}
			if !m.seek.Dragging() {
				m.seek.Value = float32(p.Position().Seconds())
			}
			if ui.Slider(c, &m.seek).Range(0, duration).Grow(1).Label("Position").Changed() {
				p.Seek(time.Duration(float64(m.seek.Value) * float64(time.Second)))
			}
			ui.Text(c, clock(p.Position())+" / "+clock(p.Duration())).Color(white)
			if ui.Button(c, "Close").Clicked() {
				m.closeClip()
			}
		})
	})
}

// clock is a time as minutes and seconds.
func clock(d time.Duration) string {
	sec := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func main() {
	m, err := newModel()
	if err != nil {
		log.Fatal(err)
	}
	err = ui.Run(app.Config{Title: "Video", Width: 640, Height: 400}, ui.PageConfig{View: m.page})
	m.waves.Close()
	if m.clip != nil {
		m.clip.Close()
	}
	if err != nil {
		log.Fatal(err)
	}
}
