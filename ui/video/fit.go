package video

import "github.com/vivaui/viva/gfx"

// Fit is how a view sizes a video's picture in its box.
type Fit uint8

const (
	// Contain shows the whole picture inside the box, centered, with its
	// proportions kept. It is the default.
	Contain Fit = iota
	// Cover fills the box with the picture, centered, with its proportions
	// kept: the parts outside the box are cut.
	Cover
	// Fill stretches the picture to the box, whatever its proportions.
	Fill
)

// fitRect is where a picture of width by height pixels is drawn in box for
// the fit. A fit that keeps the proportions gives the picture more room than
// the box where the fit is Cover; the view then clips it to the box.
func fitRect(fit Fit, box gfx.Rect, width, height int) gfx.Rect {
	if width <= 0 || height <= 0 || box.Width <= 0 || box.Height <= 0 {
		return gfx.Rect{}
	}
	if fit == Fill {
		return box
	}
	scale := box.Width / float32(width)
	if stretch := box.Height / float32(height); (fit == Cover) == (stretch > scale) {
		scale = stretch
	}
	w, h := float32(width)*scale, float32(height)*scale
	return gfx.Rect{X: box.X + (box.Width-w)/2, Y: box.Y + (box.Height-h)/2, Width: w, Height: h}
}
