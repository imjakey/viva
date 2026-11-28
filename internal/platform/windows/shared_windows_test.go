//go:build windows

package windows

import (
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/internal/platform"
)

func closeSharedHandle(handle uintptr) {
	if handle != 0 {
		syscall.CloseHandle(syscall.Handle(handle))
	}
}

// createSharedBGRA makes a width by height BGRA texture of pixels on device,
// shared by an NT handle with a keyed mutex, as a media engine's frame is.
func createSharedBGRA(device uintptr, width, height int, pixels []byte) (uintptr, error) {
	var device1 uintptr
	if err := hresult(comCall2(device, 0, uintptr(unsafe.Pointer(&iidDevice1)), uintptr(unsafe.Pointer(&device1)))); err != nil {
		return 0, err
	}
	defer release(&device1)
	description := textureDesc{
		Width: uint32(width), Height: uint32(height), Mips: 1, Array: 1, Format: 87, Samples: 1,
		Usage: 0, Bind: 0x8 | 0x20, Misc: 0x100 | 0x800,
	}
	data := resourceData{Data: unsafe.Pointer(&pixels[0]), Pitch: uint32(width * 4)}
	var texture uintptr
	if err := hresult(comCall3(device1, 5, uintptr(unsafe.Pointer(&description)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&texture)))); err != nil {
		return 0, err
	}
	defer release(&texture)
	var resource uintptr
	if err := hresult(comCall2(texture, 0, uintptr(unsafe.Pointer(&iidDXGIResource1)), uintptr(unsafe.Pointer(&resource)))); err != nil {
		return 0, err
	}
	defer release(&resource)
	var handle uintptr
	if err := hresult(comCall4(resource, 13, 0, 0x80000001, 0, uintptr(unsafe.Pointer(&handle)))); err != nil {
		return 0, err
	}
	return handle, nil
}

func redPixels(size int) []byte {
	pixels := make([]byte, size*size*4)
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 0, 0, 255, 255 // BGRA
	}
	return pixels
}

func TestOffscreenDrawsASharedTexture(t *testing.T) {
	offscreen, err := NewOffscreen(64, 64, 1, platform.Color{})
	if err != nil {
		t.Skipf("offscreen renderer unavailable: %v", err)
	}
	defer offscreen.Close()
	const size = 64
	handle, err := createSharedBGRA(offscreen.renderer.device, size, size, redPixels(size))
	if err != nil {
		t.Skipf("shared textures unavailable: %v", err)
	}
	defer closeSharedHandle(handle)
	label := platform.TextLabel{
		Width: size, Height: size,
		TextureID: 1, TextureHandle: handle,
		TextureWidth: size, TextureHeight: size, TextureVersion: 1,
	}
	if err := offscreen.Set(nil, []platform.TextLabel{label}); err != nil {
		t.Fatal(err)
	}
	drawn, err := offscreen.Image()
	if err != nil {
		t.Fatal(err)
	}
	if red, _, _, _ := drawn.At(32, 32).RGBA(); red>>8 < 0xf0 {
		t.Fatalf("pixel = %v, want the shared texture's red", drawn.At(32, 32))
	}
}

func TestOffscreenClipsASharedTexture(t *testing.T) {
	offscreen, err := NewOffscreen(64, 64, 1, platform.Color{})
	if err != nil {
		t.Skipf("offscreen renderer unavailable: %v", err)
	}
	defer offscreen.Close()
	const size = 64
	handle, err := createSharedBGRA(offscreen.renderer.device, size, size, redPixels(size))
	if err != nil {
		t.Skipf("shared textures unavailable: %v", err)
	}
	defer closeSharedHandle(handle)
	label := platform.TextLabel{
		Width: size, Height: size,
		TextureID: 1, TextureHandle: handle,
		TextureWidth: size, TextureHeight: size, TextureVersion: 1,
		Clip: gfx.Rect{Width: 10, Height: 10}, Clipped: true,
	}
	if err := offscreen.Set(nil, []platform.TextLabel{label}); err != nil {
		t.Fatal(err)
	}
	drawn, err := offscreen.Image()
	if err != nil {
		t.Fatal(err)
	}
	if red, _, _, _ := drawn.At(4, 4).RGBA(); red>>8 < 0xf0 {
		t.Fatalf("inside the clip: %v, want red", drawn.At(4, 4))
	}
	if red, _, _, _ := drawn.At(32, 32).RGBA(); red>>8 > 0x10 {
		t.Fatalf("outside the clip: %v, want the clear color", drawn.At(32, 32))
	}
}

func TestOffscreenSweepsAgedSharedTexture(t *testing.T) {
	offscreen, err := NewOffscreen(64, 64, 1, platform.Color{})
	if err != nil {
		t.Skipf("offscreen renderer unavailable: %v", err)
	}
	defer offscreen.Close()
	const size = 64
	handle, err := createSharedBGRA(offscreen.renderer.device, size, size, redPixels(size))
	if err != nil {
		t.Skipf("shared textures unavailable: %v", err)
	}
	defer closeSharedHandle(handle)
	label := platform.TextLabel{
		Width: size, Height: size,
		TextureID: 77, TextureHandle: handle,
		TextureWidth: size, TextureHeight: size, TextureVersion: 1,
	}
	if err := offscreen.Set(nil, []platform.TextLabel{label}); err != nil {
		t.Fatal(err)
	}
	if _, err := offscreen.Image(); err != nil {
		t.Fatal(err)
	}
	entry := offscreen.renderer.shared[77]
	if entry == nil {
		t.Fatal("shared texture entry was not cached")
	}
	// Back-date lastUsed past sharedSweepAge and paint an empty scene: sweepShared must drop it even after 1 frame.
	entry.lastUsed = time.Now().Add(-sharedSweepAge - time.Second)
	if err := offscreen.Set(nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := offscreen.Image(); err != nil {
		t.Fatal(err)
	}
	if _, ok := offscreen.renderer.shared[77]; ok {
		t.Fatal("aged shared texture was not swept")
	}
}
