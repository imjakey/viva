package video

import (
	"testing"

	"github.com/vivaui/viva/gfx"
)

func TestFitRect(t *testing.T) {
	box := gfx.Rect{X: 10, Y: 20, Width: 100, Height: 100}
	// A 2:1 picture in a square box.
	if got := fitRect(Contain, box, 200, 100); got != (gfx.Rect{X: 10, Y: 45, Width: 100, Height: 50}) {
		t.Fatalf("Contain = %+v", got)
	}
	if got := fitRect(Cover, box, 200, 100); got != (gfx.Rect{X: -40, Y: 20, Width: 200, Height: 100}) {
		t.Fatalf("Cover = %+v", got)
	}
	if got := fitRect(Fill, box, 200, 100); got != box {
		t.Fatalf("Fill = %+v", got)
	}
	// A 1:2 picture narrower than the box.
	tall := gfx.Rect{X: 0, Y: 0, Width: 200, Height: 50}
	if got := fitRect(Contain, tall, 100, 200); got != (gfx.Rect{X: 87.5, Width: 25, Height: 50}) {
		t.Fatalf("Contain tall = %+v", got)
	}
	if got := fitRect(Cover, tall, 100, 200); got != (gfx.Rect{Y: -175, Width: 200, Height: 400}) {
		t.Fatalf("Cover tall = %+v", got)
	}
}

func TestFitRectOfNothingIsEmpty(t *testing.T) {
	for _, test := range []struct {
		box    gfx.Rect
		width  int
		height int
	}{
		{gfx.Rect{Width: 10, Height: 10}, 0, 10},
		{gfx.Rect{Width: 10, Height: 10}, 10, 0},
		{gfx.Rect{}, 10, 10},
		{gfx.Rect{Width: -1, Height: 10}, 10, 10},
	} {
		if got := fitRect(Contain, test.box, test.width, test.height); got != (gfx.Rect{}) {
			t.Fatalf("fitRect(%+v, %d, %d) = %+v, want empty", test.box, test.width, test.height, got)
		}
	}
}
