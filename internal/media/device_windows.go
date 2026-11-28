//go:build windows && (amd64 || arm64)

package media

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemDirectory  = kernel32.NewProc("GetSystemDirectoryW")
	d3d11                   = systemDLL("d3d11.dll")
	procD3D11CreateDevice   = d3d11.NewProc("D3D11CreateDevice")
	dxgi                    = systemDLL("dxgi.dll")
	procCreateDXGIFactory1  = dxgi.NewProc("CreateDXGIFactory1")
	iidDXGIFactory1         = syscall.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidDXGIDevice           = syscall.GUID{Data1: 0x54ec77fa, Data2: 0x1377, Data3: 0x44e6, Data4: [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	rendererAdapterMu       sync.RWMutex
	rendererAdapterLUIDLow  uint32
	rendererAdapterLUIDHigh int32
	rendererAdapterKnown    bool
)

// dxgiAdapterDesc1 matches DXGI_ADAPTER_DESC1 on 64-bit Windows.
type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorId              uint32
	DeviceId              uint32
	SubSysId              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuidLow        uint32
	AdapterLuidHigh       int32
	Flags                 uint32
}

// SetRendererAdapterLUID records the DXGI adapter LUID of the window renderer's
// Direct3D 11 device so new video engines decode on the same adapter and can
// share textures across devices on multi-GPU or WARP systems.
func SetRendererAdapterLUID(low uint32, high int32) {
	rendererAdapterMu.Lock()
	rendererAdapterLUIDLow = low
	rendererAdapterLUIDHigh = high
	rendererAdapterKnown = true
	rendererAdapterMu.Unlock()
}

func rendererAdapterLUID() (low uint32, high int32, ok bool) {
	rendererAdapterMu.RLock()
	low, high, ok = rendererAdapterLUIDLow, rendererAdapterLUIDHigh, rendererAdapterKnown
	rendererAdapterMu.RUnlock()
	return low, high, ok
}

// sharedRegistry tracks live shared texture IDs back to their owning engine
// and remembers recently retired IDs so the renderer drops closed textures on
// the next frame instead of waiting for its frame-count sweep.
const retiredRingSize = 512

var sharedRegistry = struct {
	sync.RWMutex
	live       map[uint64]*mediaEngine
	retired    map[uint64]struct{}
	retiredLog [retiredRingSize]uint64
	retiredPos int
}{
	live:    make(map[uint64]*mediaEngine),
	retired: make(map[uint64]struct{}),
}

func registerSharedTexture(id uint64, e *mediaEngine) {
	if id == 0 {
		return
	}
	sharedRegistry.Lock()
	sharedRegistry.live[id] = e
	delete(sharedRegistry.retired, id)
	sharedRegistry.Unlock()
}

func retireSharedTexture(id uint64) {
	if id == 0 {
		return
	}
	sharedRegistry.Lock()
	delete(sharedRegistry.live, id)
	if old := sharedRegistry.retiredLog[sharedRegistry.retiredPos]; old != 0 {
		delete(sharedRegistry.retired, old)
	}
	sharedRegistry.retiredLog[sharedRegistry.retiredPos] = id
	sharedRegistry.retiredPos = (sharedRegistry.retiredPos + 1) % retiredRingSize
	sharedRegistry.retired[id] = struct{}{}
	sharedRegistry.Unlock()
}

// IsSharedTextureRetired reports whether the media engine that created texture
// id has already freed its slot.
func IsSharedTextureRetired(id uint64) bool {
	if id == 0 {
		return false
	}
	sharedRegistry.RLock()
	_, ok := sharedRegistry.retired[id]
	sharedRegistry.RUnlock()
	return ok
}

// ReportSharedTextureError tells the engine owning texture id that the
// renderer could not open its shared handle, so the player surfaces Failed
// through Err rather than painting black silently.
func ReportSharedTextureError(id uint64, err error) {
	if id == 0 || err == nil {
		return
	}
	sharedRegistry.RLock()
	e := sharedRegistry.live[id]
	sharedRegistry.RUnlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.closed || e.state == Failed {
		e.mu.Unlock()
		return
	}
	e.state = Failed
	e.failure = fmt.Errorf("video: renderer could not open shared texture: %w", err)
	e.mu.Unlock()
	e.wakeWindow()
}

// systemDLL loads name from the Windows system directory by absolute path, so
// no other directory's DLL with the same name is found first.
func systemDLL(name string) *syscall.LazyDLL {
	var path [32768]uint16
	n, _, _ := procGetSystemDirectory.Call(uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)))
	if n == 0 || n >= uintptr(len(path)) {
		return syscall.NewLazyDLL(name)
	}
	return syscall.NewLazyDLL(syscall.UTF16ToString(path[:n]) + `\` + name)
}

// newVideoDevice creates the Direct3D 11 device Media Foundation decodes on:
// matching the renderer's DXGI adapter when known so shared NT handles always
// open on the same GPU (or WARP), with video support where available and
// falling back to a plain device otherwise. The caller releases both.
func newVideoDevice() (device, context uintptr, err error) {
	const videoSupport = 0x800
	if low, high, ok := rendererAdapterLUID(); ok {
		if adapter := findAdapterByLUID(low, high); adapter != 0 {
			defer releaseCom(&adapter)
			if device, context, err = createDeviceOnAdapter(adapter, videoSupport); err == nil {
				return device, context, nil
			}
			if device, context, err = createDeviceOnAdapter(adapter, 0); err == nil {
				return device, context, nil
			}
		}
	}
	if device, context, err = createVideoDevice(videoSupport); err == nil {
		return device, context, nil
	}
	return createVideoDevice(0)
}

// findAdapterByLUID enumerates DXGI 1.1 adapters (including the software WARP
// adapter) and returns the retained IDXGIAdapter1 whose LUID matches, or 0.
func findAdapterByLUID(low uint32, high int32) uintptr {
	if procCreateDXGIFactory1.Find() != nil {
		return 0
	}
	var factory uintptr
	hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
	if hresultFailed(hr) || factory == 0 {
		return 0
	}
	defer releaseCom(&factory)
	const (
		factoryEnumAdapters1 = 12
		adapterGetDesc1      = 10
	)
	for i := uintptr(0); ; i++ {
		var adapter uintptr
		if hresultFailed(invoke(factory, factoryEnumAdapters1, i, uintptr(unsafe.Pointer(&adapter)))) || adapter == 0 {
			return 0
		}
		var desc dxgiAdapterDesc1
		if !hresultFailed(invoke(adapter, adapterGetDesc1, uintptr(unsafe.Pointer(&desc)))) &&
			desc.AdapterLuidLow == low && desc.AdapterLuidHigh == high {
			return adapter
		}
		releaseCom(&adapter)
	}
}

// createDeviceOnAdapter creates a Direct3D 11 device on a specific IDXGIAdapter.
// D3D_DRIVER_TYPE_UNKNOWN (0) is required whenever a non-null adapter is passed.
func createDeviceOnAdapter(adapter, videoSupport uintptr) (device, context uintptr, err error) {
	if err := procD3D11CreateDevice.Find(); err != nil {
		return 0, 0, err
	}
	levels := [...]uint32{0xb000, 0xa100, 0xa000} // 11_0, 10_1, 10_0
	const (
		driverUnknown = 0
		bgraSupport   = 0x20
		sdkVersion    = 7
	)
	var level uint32
	hr, _, _ := procD3D11CreateDevice.Call(adapter, driverUnknown, 0, bgraSupport|videoSupport,
		uintptr(unsafe.Pointer(&levels[0])), uintptr(len(levels)), sdkVersion,
		uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&context)))
	if int32(uint32(hr)) >= 0 {
		return device, context, nil
	}
	return 0, 0, fmt.Errorf("video: cannot create a Direct3D device on adapter: %#x", uint32(hr))
}

// createVideoDevice creates the device from the first driver of hardware
// and the software rasterizer that takes flags.
func createVideoDevice(videoSupport uintptr) (device, context uintptr, err error) {
	if err := procD3D11CreateDevice.Find(); err != nil {
		return 0, 0, err
	}
	levels := [...]uint32{0xb000, 0xa100, 0xa000} // 11_0, 10_1, 10_0
	const bgraSupport, sdkVersion = 0x20, 7
	const hardware, warp = 1, 5
	drivers := []uintptr{hardware, warp}
	if os.Getenv("VIVA_SOFTWARE_RENDERING") == "1" {
		drivers = drivers[1:]
	}
	var hr uintptr
	for _, driver := range drivers {
		var level uint32
		hr, _, _ = procD3D11CreateDevice.Call(0, driver, 0, bgraSupport|videoSupport,
			uintptr(unsafe.Pointer(&levels[0])), uintptr(len(levels)), sdkVersion,
			uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&context)))
		if int32(uint32(hr)) >= 0 {
			return device, context, nil
		}
		device, context = 0, 0
	}
	return 0, 0, fmt.Errorf("video: cannot create a Direct3D device: %#x", uint32(hr))
}
