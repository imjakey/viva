package platform

import "testing"

func TestTextureLabelsAreValidated(t *testing.T) {
	valid := TextLabel{
		X: 0, Y: 0, Width: 100, Height: 50,
		TextureHandle: 0x1234, TextureID: 7, TextureWidth: 640, TextureHeight: 360, TextureVersion: 3,
	}
	if err := ValidateTextLabel(0, valid); err != nil {
		t.Fatalf("a valid texture label: %v", err)
	}
	tests := []struct {
		name   string
		change func(*TextLabel)
	}{
		{"no handle", func(l *TextLabel) { l.TextureHandle = 0 }},
		{"no width", func(l *TextLabel) { l.Width = 0 }},
		{"no height", func(l *TextLabel) { l.Height = -1 }},
		{"no texture width", func(l *TextLabel) { l.TextureWidth = 0 }},
		{"no texture height", func(l *TextLabel) { l.TextureHeight = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label := valid
			test.change(&label)
			if err := ValidateTextLabel(0, label); err == nil {
				t.Fatal("an invalid texture label passed validation")
			}
		})
	}
	// A bitmap label is still validated for its payload.
	broken := TextLabel{Width: 10, Height: 10, Bitmap: []byte{1, 2, 3}, BitmapWidth: 4, BitmapHeight: 4, BitmapStride: 16}
	if err := ValidateTextLabel(0, broken); err == nil {
		t.Fatal("a short bitmap payload passed validation")
	}
}
