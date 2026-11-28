package gfx

import "testing"

func TestDrawTextureRecordsItsBounds(t *testing.T) {
	var canvas Canvas
	texture := NativeTexture{ID: 7, Handle: 0x1234, Width: 640, Height: 360, Version: 3}
	canvas.DrawTexture(&texture, Rect{X: 10, Y: 20, Width: 80, Height: 45}, 0.25)
	images := canvas.Images()
	if len(images) != 1 {
		t.Fatalf("recorded %d images, want 1", len(images))
	}
	image := images[0]
	if image.Texture == nil {
		t.Fatal("the image has no texture")
	}
	if *image.Texture != texture {
		t.Fatalf("texture = %+v, want %+v", *image.Texture, texture)
	}
	if image.Bounds != (Rect{X: 10, Y: 20, Width: 80, Height: 45}) {
		t.Errorf("bounds = %+v", image.Bounds)
	}
	if image.Fade != 0.25 {
		t.Errorf("fade = %g, want 0.25", image.Fade)
	}
	// The recorded image is immutable: changing the caller's value does not
	// change what the canvas holds.
	texture.Version = 99
	if images[0].Texture.Version != 3 {
		t.Error("the canvas kept a pointer to the caller's texture")
	}
}

func TestDrawTextureIgnoresInvalidInput(t *testing.T) {
	var canvas Canvas
	valid := NativeTexture{ID: 1, Handle: 2, Width: 4, Height: 4}
	noHandle := valid
	noHandle.Handle = 0
	noWidth := valid
	noWidth.Width = 0
	noID := valid
	noID.ID = 0
	canvas.DrawTexture(nil, Rect{Width: 4, Height: 4}, 0)
	canvas.DrawTexture(&noHandle, Rect{Width: 4, Height: 4}, 0)
	canvas.DrawTexture(&noID, Rect{Width: 4, Height: 4}, 0)
	canvas.DrawTexture(&noWidth, Rect{Width: 4, Height: 4}, 0)
	canvas.DrawTexture(&valid, Rect{Width: 0, Height: 4}, 0)
	canvas.DrawTexture(&valid, Rect{Width: 4, Height: -1}, 0)
	if got := len(canvas.Images()); got != 0 {
		t.Fatalf("recorded %d images, want 0", got)
	}
}

func TestDrawTextureClampsItsFade(t *testing.T) {
	var canvas Canvas
	texture := NativeTexture{ID: 1, Handle: 2, Width: 4, Height: 4}
	canvas.DrawTexture(&texture, Rect{Width: 4, Height: 4}, -1)
	canvas.DrawTexture(&texture, Rect{Width: 4, Height: 4}, 2)
	images := canvas.Images()
	if len(images) != 2 {
		t.Fatalf("recorded %d images, want 2", len(images))
	}
	if images[0].Fade != 0 || images[1].Fade != 1 {
		t.Errorf("fades = %g, %g, want 0, 1", images[0].Fade, images[1].Fade)
	}
}
