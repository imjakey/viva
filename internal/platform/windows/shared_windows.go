package windows

import (
	"errors"
	"time"
	"unsafe"

	"github.com/vivaui/viva/internal/media"
	"github.com/vivaui/viva/internal/platform"
)

// A shared texture is one a media engine decoded a picture into, in BGRA,
// on a device of its own, which this renderer opens on its device by an NT
// handle and reads under the texture's keyed mutex. Opening is a per-texture
// cost, so entries are kept by texture ID and swept promptly once retired by
// the engine or after an idle window.

const (
	deviceOpenSharedResource1 = 48         // ID3D11Device1
	keyedAcquireSync          = 8          // IDXGIKeyedMutex
	keyedReleaseSync          = 9          // IDXGIKeyedMutex
	sharedWaitAbandoned       = 0x80       // WAIT_ABANDONED: caller owns mutex, contents may be invalid
	sharedWaitTimeout         = 0x102      // WaitForSingleObject's timeout HRESULT
	dxgiErrorDeviceRemoved    = 0x887a0005 // DXGI_ERROR_DEVICE_REMOVED
	dxgiErrorDeviceReset      = 0x887a0007 // DXGI_ERROR_DEVICE_RESET
	sharedAcquireTimeout      = 100        // ms a frame waits for the engine to finish writing
	sharedSweepFrames         = 240        // frames an unused texture stays open
	sharedSweepAge            = 5 * time.Second
)

var (
	iidDevice1       = guid{0xa04bfb29, 0x08ef, 0x43d6, [8]byte{0xa4, 0x9c, 0xa9, 0xbd, 0xbd, 0xcb, 0xe6, 0x86}}
	iidKeyedMutex    = guid{0x9d8e1289, 0xd7b3, 0x465f, [8]byte{0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d}}
	iidDXGIResource1 = guid{0x30961379, 0x4609, 0x4a41, [8]byte{0x99, 0x8e, 0x54, 0xfe, 0x56, 0x7e, 0xe0, 0xc1}}
	iidDXGIDevice    = guid{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
)

// dxgiAdapterDesc matches DXGI_ADAPTER_DESC on 64-bit Windows.
type dxgiAdapterDesc struct {
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
}

// publishAdapterLUID records this renderer's DXGI adapter LUID in internal/media
// so newly opened video engines create their decode device on the same adapter.
func (renderer *renderer) publishAdapterLUID() {
	if renderer.device == 0 {
		return
	}
	var dxgiDevice uintptr
	if hresult(comCall2(renderer.device, 0, uintptr(unsafe.Pointer(&iidDXGIDevice)), uintptr(unsafe.Pointer(&dxgiDevice)))) != nil || dxgiDevice == 0 {
		return
	}
	defer release(&dxgiDevice)
	var adapter uintptr
	if hresult(comCall1(dxgiDevice, 7, uintptr(unsafe.Pointer(&adapter)))) != nil || adapter == 0 {
		return
	}
	defer release(&adapter)
	var desc dxgiAdapterDesc
	if hresult(comCall1(adapter, 8, uintptr(unsafe.Pointer(&desc)))) == nil {
		media.SetRendererAdapterLUID(desc.AdapterLuidLow, desc.AdapterLuidHigh)
	}
}

// sharedTexture is a texture a media engine decoded into, opened on the
// renderer's device and read under its keyed mutex.
type sharedTexture struct {
	id       uint64
	handle   uintptr
	width    int
	height   int
	view     uintptr // ID3D11ShaderResourceView
	mutex    uintptr // IDXGIKeyedMutex
	frame    uint64  // the last frame that acquired it
	lastUsed time.Time
}

func (texture *sharedTexture) close() {
	release(&texture.view)
	release(&texture.mutex)
}

// sharedView returns the view of a label's shared texture, opening it the
// first time and acquiring its keyed mutex for this frame, or nil when the
// texture cannot be opened or the engine is still writing it. The frame's
// mutexes are released by releaseShared once its draws are issued.
func (renderer *renderer) sharedView(label platform.TextLabel) *sharedTexture {
	if label.TextureHandle == 0 || label.TextureID == 0 {
		return nil
	}
	if renderer.device1 == 0 {
		media.ReportSharedTextureError(label.TextureID, errors.New("ID3D11Device1 is unavailable"))
		return nil
	}
	entry := renderer.shared[label.TextureID]
	if entry != nil && (entry.handle != label.TextureHandle || entry.width != label.TextureWidth || entry.height != label.TextureHeight) {
		entry.close()
		delete(renderer.shared, label.TextureID)
		entry = nil
	}
	if entry == nil {
		opened, status, err := renderer.openShared(label)
		if err != nil {
			// A device-removed/reset error means the renderer is about to
			// recreate its own device; report every other failure so the
			// player surfaces Err instead of painting black silently.
			code := uint32(status)
			if code != dxgiErrorDeviceRemoved && code != dxgiErrorDeviceReset {
				media.ReportSharedTextureError(label.TextureID, err)
			}
			return nil
		}
		if renderer.shared == nil {
			renderer.shared = make(map[uint64]*sharedTexture)
		}
		renderer.shared[label.TextureID] = opened
		entry = opened
	}
	return renderer.acquireShared(entry)
}

// acquireShared acquires entry's keyed mutex for the current frame.
func (renderer *renderer) acquireShared(entry *sharedTexture) *sharedTexture {
	if entry == nil {
		return nil
	}
	if entry.frame == renderer.frameNumber {
		return entry
	}
	status := comCall2(entry.mutex, keyedAcquireSync, 0, sharedAcquireTimeout)
	if status == sharedWaitAbandoned {
		// Ownership was granted, so release immediately, but skip drawing
		// potentially inconsistent pixels.
		comCall1(entry.mutex, keyedReleaseSync, 0)
		return nil
	}
	if err := hresult(status); err != nil || status == sharedWaitTimeout {
		return nil
	}
	entry.frame = renderer.frameNumber
	entry.lastUsed = time.Now()
	renderer.acquired = append(renderer.acquired, entry)
	return entry
}

// openShared opens a label's texture on the renderer's device and makes the
// view and keyed mutex it is read through.
func (renderer *renderer) openShared(label platform.TextLabel) (*sharedTexture, uintptr, error) {
	var texture uintptr
	status := comCall3(renderer.device1, deviceOpenSharedResource1, label.TextureHandle, uintptr(unsafe.Pointer(&textureIID)), uintptr(unsafe.Pointer(&texture)))
	if err := hresult(status); err != nil {
		return nil, status, err
	}
	defer release(&texture)
	entry := &sharedTexture{id: label.TextureID, handle: label.TextureHandle, width: label.TextureWidth, height: label.TextureHeight}
	status = comCall2(texture, 0, uintptr(unsafe.Pointer(&iidKeyedMutex)), uintptr(unsafe.Pointer(&entry.mutex)))
	if err := hresult(status); err != nil {
		return nil, status, err
	}
	status = comCall3(renderer.device, 7, texture, 0, uintptr(unsafe.Pointer(&entry.view)))
	if err := hresult(status); err != nil {
		release(&entry.mutex)
		return nil, status, err
	}
	return entry, 0, nil
}

// drawSharedTexture draws a shared-texture label with the text pipeline's
// quad shader, narrowed to its scissor. If the latest slot's keyed mutex times
// out, it falls back to the last valid slot drawn by this label to avoid a
// blank-frame flicker.
func (renderer *renderer) drawSharedTexture(label *labelTexture, scale float32, width, height int, frame *frameConstants, current *scissorRect) {
	l := label.label
	if label.picture != nil || label.view != 0 || label.texture != 0 {
		// A slot that showed text or a bitmap before keeps nothing of it.
		label.dropTexture()
	}
	entry := renderer.sharedView(l)
	if entry == nil && label.lastSharedID != 0 && label.lastSharedID != l.TextureID && !media.IsSharedTextureRetired(label.lastSharedID) {
		entry = renderer.acquireShared(renderer.shared[label.lastSharedID])
	}
	if entry == nil {
		return
	}
	label.lastSharedID = entry.id
	scissor, visible := labelScissor(l, scale, width, height)
	if !visible {
		return
	}
	x, y := platform.DevicePosition(l.X, 0, scale), platform.DevicePosition(l.Y, 0, scale)
	w, h := platform.DeviceExtent(l.X, l.Width, scale), platform.DeviceExtent(l.Y, l.Height, scale)
	frame.Bounds = [4]float32{float32(x) / scale, float32(y) / scale, float32(w) / scale, float32(h) / scale}
	frame.TextShape = [4]float32{l.Width, l.Height, l.ImageRadius, l.ImageFade}
	frame.TextMode = 0
	comCall6(renderer.context, 48, renderer.constants, 0, 0, uintptr(unsafe.Pointer(frame)), 0, 0)
	if scissor != *current {
		renderer.setScissor(scissor)
		*current = scissor
	}
	comCall3(renderer.context, 8, 1, 1, uintptr(unsafe.Pointer(&entry.view)))
	comCall2(renderer.context, 13, 6, 0)
}

// releaseShared releases the keyed mutexes the frame read, its draws issued:
// the GPU then reads the texture after the engine's writes, and the engine
// may write it again after the frame's reads.
func (renderer *renderer) releaseShared() {
	for _, entry := range renderer.acquired {
		comCall1(entry.mutex, keyedReleaseSync, 0)
	}
	renderer.acquired = renderer.acquired[:0]
}

// sweepShared closes shared textures that the engine has retired, or that no
// frame has read within the frame or time window, or all of them when the
// device is going.
func (renderer *renderer) sweepShared(all bool) {
	now := time.Now()
	for id, entry := range renderer.shared {
		retired := media.IsSharedTextureRetired(id)
		aged := !entry.lastUsed.IsZero() && now.Sub(entry.lastUsed) > sharedSweepAge
		if all || retired || renderer.frameNumber-entry.frame > sharedSweepFrames || aged {
			entry.close()
			delete(renderer.shared, id)
		}
	}
}
