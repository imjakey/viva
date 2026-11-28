//go:build windows && (amd64 || arm64)

package media

import (
	"testing"
	"unsafe"
)

func deviceAdapterLUID(t *testing.T, device uintptr) (uint32, int32) {
	t.Helper()
	var dxgiDevice uintptr
	if hresultFailed(invoke(device, comQueryInterface, uintptr(unsafe.Pointer(&iidDXGIDevice)), uintptr(unsafe.Pointer(&dxgiDevice)))) || dxgiDevice == 0 {
		t.Fatal("QueryInterface(IDXGIDevice) failed")
	}
	defer releaseCom(&dxgiDevice)
	var adapter uintptr
	if hresultFailed(invoke(dxgiDevice, 7, uintptr(unsafe.Pointer(&adapter)))) || adapter == 0 {
		t.Fatal("IDXGIDevice.GetAdapter failed")
	}
	defer releaseCom(&adapter)
	var desc dxgiAdapterDesc1
	// IDXGIAdapter::GetDesc (slot 8) populates the identical prefix up to AdapterLuid.
	if hresultFailed(invoke(adapter, 8, uintptr(unsafe.Pointer(&desc)))) {
		t.Fatal("IDXGIAdapter.GetDesc failed")
	}
	return desc.AdapterLuidLow, desc.AdapterLuidHigh
}

func TestNewVideoDevice(t *testing.T) {
	device, context, err := newVideoDevice()
	if err != nil {
		t.Skipf("no Direct3D device: %v", err)
	}
	if device == 0 || context == 0 {
		t.Fatalf("device = %#x, context = %#x", device, context)
	}
	low, high := deviceAdapterLUID(t, device)
	releaseCom(&context)
	releaseCom(&device)

	// Registering the renderer's adapter LUID makes newVideoDevice select the same adapter.
	SetRendererAdapterLUID(low, high)
	device2, context2, err := newVideoDevice()
	if err != nil {
		t.Fatalf("newVideoDevice with LUID: %v", err)
	}
	defer releaseCom(&context2)
	defer releaseCom(&device2)
	low2, high2 := deviceAdapterLUID(t, device2)
	if low2 != low || high2 != high {
		t.Fatalf("adapter LUID = (%d, %d), want (%d, %d)", low2, high2, low, high)
	}
}
