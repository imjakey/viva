//go:build windows

package framebuild

import (
	"testing"

	"github.com/vivaui/viva/gfx"
)

func TestTextureImageBecomesALabel(t *testing.T) {
	var canvas gfx.Canvas
	texture := gfx.NativeTexture{ID: 5, Handle: 0x99, Width: 64, Height: 32, Version: 4}
	canvas.DrawTexture(&texture, gfx.Rect{X: 10, Y: 20, Width: 80, Height: 40}, 0.5)
	builder := New(&canvas)
	frame, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.TextLabels) != 1 {
		t.Fatalf("frame has %d labels, want 1", len(frame.TextLabels))
	}
	label := frame.TextLabels[0]
	if label.TextureID != 5 || label.TextureHandle != 0x99 || label.TextureWidth != 64 || label.TextureHeight != 32 || label.TextureVersion != 4 {
		t.Fatalf("label texture = %+v", label)
	}
	if label.Bitmap != nil || label.BitmapUpload {
		t.Fatalf("a texture label carried pixels: %+v", label)
	}
	if label.X != 10 || label.Y != 20 || label.Width != 80 || label.Height != 40 || label.ImageFade != 0.5 {
		t.Fatalf("label = %+v", label)
	}
}

func TestClippedTextureKeepsItsBounds(t *testing.T) {
	var canvas gfx.Canvas
	texture := gfx.NativeTexture{ID: 5, Handle: 0x99, Width: 64, Height: 32}
	clip := gfx.Rect{X: 20, Y: 20, Width: 40, Height: 20}
	canvas.Clip(clip)
	canvas.DrawTexture(&texture, gfx.Rect{X: 10, Y: 20, Width: 80, Height: 40}, 0)
	canvas.Unclip()
	builder := New(&canvas)
	frame, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.TextLabels) != 1 {
		t.Fatalf("frame has %d labels, want 1", len(frame.TextLabels))
	}
	label := frame.TextLabels[0]
	if label.X != 10 || label.Width != 80 || label.Height != 40 {
		t.Fatalf("a clipped texture was cut in Go: %+v", label)
	}
	if !label.Clipped || label.Clip != clip {
		t.Fatalf("clip = %v %+v, want %+v", label.Clipped, label.Clip, clip)
	}
}

func TestClippedAwayTextureIsDropped(t *testing.T) {
	var canvas gfx.Canvas
	texture := gfx.NativeTexture{ID: 5, Handle: 0x99, Width: 64, Height: 32}
	canvas.Clip(gfx.Rect{X: 200, Y: 200, Width: 10, Height: 10})
	canvas.DrawTexture(&texture, gfx.Rect{X: 10, Y: 20, Width: 80, Height: 40}, 0)
	canvas.Unclip()
	builder := New(&canvas)
	frame, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.TextLabels) != 0 {
		t.Fatalf("a texture outside its clip was kept: %+v", frame.TextLabels)
	}
}

func TestSameImageBitmapComparesTextures(t *testing.T) {
	a := gfx.Image{Texture: &gfx.NativeTexture{ID: 1, Handle: 2, Width: 4, Height: 4, Version: 1}}
	b := gfx.Image{Texture: &gfx.NativeTexture{ID: 1, Handle: 2, Width: 4, Height: 4, Version: 1}}
	if !SameImageBitmap(a, b) {
		t.Fatal("the same texture is not the same picture")
	}
	b.Texture.Version = 2
	if SameImageBitmap(a, b) {
		t.Fatal("a new texture version is not a new picture")
	}
	b.Texture.Version = 1
	b.Texture.Handle = 3
	if SameImageBitmap(a, b) {
		t.Fatal("a reopened texture is not a new picture")
	}
	if SameImageBitmap(a, gfx.Image{}) {
		t.Fatal("a texture and no image are the same picture")
	}
	bitmap := gfx.Image{Bitmap: opaqueBitmap(3, 1, 4, 4)}
	if SameImageBitmap(a, bitmap) || SameImageBitmap(bitmap, a) {
		t.Fatal("a texture and a bitmap are the same picture")
	}
}
