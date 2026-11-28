//go:build windows && (amd64 || arm64)

package media

// On Windows, Media Foundation plays the video. An IMFMediaEngine decodes it
// and keeps its clock, with sound, on a Direct3D 11 device this package makes;
// the engine converts the picture due into one of three BGRA textures, each
// shared with the window's renderer by an NT handle and ordered by a keyed
// mutex, so the picture reaches the window where it was decoded, on the GPU.
//
// The engine reports what it does through events, from threads of its own.
// A small COM object, made once, carries them to Go and wakes the window.

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// frameSlots is the number of textures the engine rotates through, leaving
// the renderer time to read one while the engine fills the next.
const frameSlots = 3

var (
	mfOnce sync.Once
	mfErr  error

	mfplat  = systemDLL("mfplat.dll")
	combase = systemDLL("ole32.dll")
	oleauto = systemDLL("oleaut32.dll")
	shlwapi = systemDLL("shlwapi.dll")

	// mfAPI holds the entry points the engine calls, resolved once.
	mfAPI struct {
		startup       *syscall.LazyProc
		deviceManager *syscall.LazyProc
		attributes    *syscall.LazyProc
		streamOver    *syscall.LazyProc
		initCOM       *syscall.LazyProc
		uninitCOM     *syscall.LazyProc
		createCOM     *syscall.LazyProc
		alloc         *syscall.LazyProc
		freeMemory    *syscall.LazyProc
		allocString   *syscall.LazyProc
		freeString    *syscall.LazyProc
		memoryStream  *syscall.LazyProc
	}

	// notifyMethods is the vtable of the engine's IMFMediaEngineNotify
	// object. Its refcount frees the object when both Go and Media
	// Foundation have released it.
	notifyMethods [4]uintptr

	// liveEngines finds the engine whose notify object is the this pointer of
	// a callback.
	liveEngines = struct {
		sync.RWMutex
		byNotify map[uintptr]*mediaEngine
	}{byNotify: make(map[uintptr]*mediaEngine)}

	// sharedIDs numbers the shared textures, for the renderer to tell one
	// from another.
	sharedIDs atomic.Uint64
)

// notifyObject is the COM layout of the engine's IMFMediaEngineNotify object,
// allocated with CoTaskMemAlloc and freed when refs drops to zero.
type notifyObject struct {
	vtbl uintptr
	refs int32
}

var (
	clsidEngineFactory = syscall.GUID{Data1: 0xb44392da, Data2: 0x499b, Data3: 0x446b, Data4: [8]byte{0xa4, 0xcb, 0x00, 0x5f, 0xea, 0xd0, 0xe6, 0xd5}}
	iidEngineFactory   = syscall.GUID{Data1: 0x4d645ace, Data2: 0x26aa, Data3: 0x4688, Data4: [8]byte{0x9b, 0xe1, 0xdf, 0x35, 0x16, 0x99, 0x0b, 0x93}}
	iidNotify          = syscall.GUID{Data1: 0xfee7c112, Data2: 0xe776, Data3: 0x42b5, Data4: [8]byte{0x9b, 0xbf, 0x00, 0x48, 0x52, 0x4e, 0x2b, 0xd5}}
	iidEngineEx        = syscall.GUID{Data1: 0x83015ead, Data2: 0xb1e6, Data3: 0x40d0, Data4: [8]byte{0xa9, 0x8a, 0x37, 0x14, 0x5f, 0xfe, 0x1a, 0xd1}}
	iidUnknown         = syscall.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidMultithread     = syscall.GUID{Data1: 0x9b7e4e00, Data2: 0x342c, Data3: 0x4106, Data4: [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0}}
	iidKeyedMutex      = syscall.GUID{Data1: 0x9d8e1289, Data2: 0xd7b3, Data3: 0x465f, Data4: [8]byte{0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d}}
	iidResource1       = syscall.GUID{Data1: 0x30961379, Data2: 0x4609, Data3: 0x4a41, Data4: [8]byte{0x99, 0x8e, 0x54, 0xfe, 0x56, 0x7e, 0xe0, 0xc1}}

	mfEngineCallback  = syscall.GUID{Data1: 0xc60381b8, Data2: 0x83a4, Data3: 0x41f8, Data4: [8]byte{0xa3, 0xd0, 0xde, 0x05, 0x07, 0x68, 0x49, 0xa9}}
	mfEngineDXGI      = syscall.GUID{Data1: 0x065702da, Data2: 0x1094, Data3: 0x486d, Data4: [8]byte{0x86, 0x17, 0xee, 0x7c, 0xc4, 0xee, 0x46, 0x48}}
	mfEngineVideoType = syscall.GUID{Data1: 0x5066893c, Data2: 0x8cf9, Data3: 0x42bc, Data4: [8]byte{0x8b, 0x8a, 0x47, 0x22, 0x12, 0xe5, 0x27, 0x26}}
)

// Places of the methods the engine calls in the vtables of its interfaces.
const (
	comQueryInterface = 0
	comRelease        = 2

	attrSetUint32  = 21
	attrSetUnknown = 27

	managerReset = 7

	factoryCreate = 3

	engineSetSource       = 6
	engineIsSeeking       = 15
	engineCurrentTime     = 16
	engineSetCurrentTime  = 17
	engineDuration        = 19
	engineIsPaused        = 20
	engineDefaultRate     = 22
	enginePlaybackRate    = 24
	engineSetLoop         = 31
	enginePlay            = 32
	enginePause           = 33
	engineSetMuted        = 35
	engineSetVolume       = 37
	engineHasVideo        = 38
	engineNativeSize      = 40
	engineShutdown        = 42
	engineTransferFrame   = 43
	engineVideoStreamTick = 44
	engineExSourceFromMem = 45
	engineExFrameStep     = 51

	multithreadSetProtected = 5

	deviceCreateTexture = 5

	keyedMutexAcquire = 8
	keyedMutexRelease = 9

	resourceSharedHandle = 13
)

// The events Media Foundation reports (MF_MEDIA_ENGINE_EVENT).
const (
	eventError         = 5
	eventLoadedData    = 11
	eventWaiting       = 12
	eventPlaying       = 13
	eventCanPlay       = 14
	eventSeeked        = 17
	eventEnded         = 19
	eventFrameStepDone = 1007
)

const (
	eNoInterface  = 0x80004002
	waitAbandoned = 0x80
	waitTimeout   = 0x102
	// seekRetries caps how often a seek is repeated when Media Foundation
	// lands it where the video was rather than where it was asked to go.
	seekRetries = 3
)

// texture2DDesc is D3D11_TEXTURE2D_DESC.
type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize uint32
	Format, SampleCount, SampleQuality  uint32
	Usage, BindFlags, CPUAccessFlags    uint32
	MiscFlags                           uint32
}

// hresultFailed reports an HRESULT of failure.
func hresultFailed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

// ptrAt is the pointer at address a, of native memory.
func ptrAt(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// vfunc is the address of method i in obj's vtable.
func vfunc(obj uintptr, i int) uintptr {
	vtbl := *(*uintptr)(ptrAt(obj))
	return *(*uintptr)(ptrAt(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

// invoke calls method i of the COM object obj, returning its HRESULT. The
// pointers passed as uintptr in args stay alive until it returns
// (go:uintptrescapes).
//
//go:uintptrescapes
func invoke(obj uintptr, i int, args ...uintptr) uintptr {
	call := make([]uintptr, 0, len(args)+1)
	call = append(call, obj)
	call = append(call, args...)
	hr, _, _ := syscall.SyscallN(vfunc(obj, i), call...)
	return hr
}

// releaseCom releases the COM object *p and forgets it.
func releaseCom(p *uintptr) {
	if *p != 0 {
		invoke(*p, comRelease)
		*p = 0
	}
}

func uintptrOf(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

func loadMediaFoundation() error {
	mfOnce.Do(func() {
		mfAPI.startup = mfplat.NewProc("MFStartup")
		mfAPI.deviceManager = mfplat.NewProc("MFCreateDXGIDeviceManager")
		mfAPI.attributes = mfplat.NewProc("MFCreateAttributes")
		mfAPI.streamOver = mfplat.NewProc("MFCreateMFByteStreamOnStream")
		mfAPI.initCOM = combase.NewProc("CoInitializeEx")
		mfAPI.uninitCOM = combase.NewProc("CoUninitialize")
		mfAPI.createCOM = combase.NewProc("CoCreateInstance")
		mfAPI.alloc = combase.NewProc("CoTaskMemAlloc")
		mfAPI.freeMemory = combase.NewProc("CoTaskMemFree")
		mfAPI.allocString = oleauto.NewProc("SysAllocString")
		mfAPI.freeString = oleauto.NewProc("SysFreeString")
		mfAPI.memoryStream = shlwapi.NewProc("SHCreateMemStream")
		for _, proc := range [...]*syscall.LazyProc{
			mfAPI.startup, mfAPI.deviceManager, mfAPI.attributes, mfAPI.streamOver,
			mfAPI.createCOM, mfAPI.allocString, mfAPI.memoryStream,
		} {
			if err := proc.Find(); err != nil {
				// Windows Server without its Media Foundation feature, or an
				// N edition without the Media Feature Pack.
				mfErr = fmt.Errorf("%w: %v", ErrUnsupported, err)
				return
			}
		}
		const mfVersion, mfStartupFull = 0x00020070, 0
		if hr, _, _ := mfAPI.startup.Call(mfVersion, mfStartupFull); hresultFailed(hr) {
			mfErr = fmt.Errorf("%w: MFStartup: %#x", ErrUnsupported, uint32(hr))
			return
		}
		if err := initFloatThunks(); err != nil {
			mfErr = err
			return
		}
		notifyMethods = [4]uintptr{
			syscall.NewCallback(notifyQueryInterface),
			syscall.NewCallback(notifyAddRef),
			syscall.NewCallback(notifyRelease),
			syscall.NewCallback(notifyDispatch),
		}
	})
	return mfErr
}

func notifyQueryInterface(this, riid, out uintptr) uintptr {
	want := *(*syscall.GUID)(ptrAt(riid))
	if want == iidUnknown || want == iidNotify {
		*(*uintptr)(ptrAt(out)) = this
		notifyAddRef(this)
		return 0 // S_OK
	}
	*(*uintptr)(ptrAt(out)) = 0
	return eNoInterface
}

func notifyAddRef(this uintptr) uintptr {
	obj := (*notifyObject)(ptrAt(this))
	return uintptr(atomic.AddInt32(&obj.refs, 1))
}

// notifyRelease drops one reference to the COM notify object, freeing it once
// both Go and Media Foundation have released their references so asynchronous
// callbacks after Shutdown never touch freed memory.
func notifyRelease(this uintptr) uintptr {
	obj := (*notifyObject)(ptrAt(this))
	n := atomic.AddInt32(&obj.refs, -1)
	if n == 0 {
		liveEngines.Lock()
		delete(liveEngines.byNotify, this)
		liveEngines.Unlock()
		mfAPI.freeMemory.Call(this)
	}
	return uintptr(n)
}

func notifyDispatch(this, event, param1, param2 uintptr) uintptr {
	liveEngines.RLock()
	e := liveEngines.byNotify[this]
	liveEngines.RUnlock()
	if e != nil {
		e.dispatch(uint32(event), param1, uint32(param2))
	}
	return 0
}

// mediaEngine plays one video with Media Foundation.
type mediaEngine struct {
	engine    uintptr // IMFMediaEngine
	extended  uintptr // IMFMediaEngineEx, the wider interface over it
	notify    uintptr // the IMFMediaEngineNotify object, in memory COM's
	device    uintptr // the Direct3D 11 device the engine decodes on
	context   uintptr
	manager   uintptr
	protected uintptr // the device's ID3D10Multithread, when it has one

	slots         [frameSlots]frameSlot
	shown         int // the slot Frame last handed out, -1 before the first
	width, height int

	// Media Foundation's threads write the fields below under mu, and the
	// UI thread reads them there.
	mu          sync.Mutex
	wake        func()
	state       State
	failure     error
	repeat      bool
	seekTarget  time.Duration
	seekStart   time.Time // when the seek in flight was asked for
	landed      bool      // a seek landed and no picture has come since
	stepDone    bool      // a frame step finished since Frame took one
	firstLoaded bool      // the engine has the data of the first picture
	pollPending bool      // a wake for the first picture is scheduled
	closed      bool

	// How Frame follows the pictures and the seeks, on the UI thread.
	pictured bool // a picture came since the last seek
	reseek   bool // seek again once the step in flight is done
	retries  int  // how often the current Seek sought again
}

// frameSlot is one texture the engine converts pictures into, the keyed mutex
// that orders its use with the renderer, and the NT handle the renderer opens.
type frameSlot struct {
	id            uint64
	tex, mutex    uintptr
	handle        uintptr
	width, height int
}

// Open opens a video with Media Foundation.
func Open(s Source) (Engine, error) {
	if err := loadMediaFoundation(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Initialize COM in MTA for CoCreateInstance on this thread. If the caller
	// is already on an STA thread (such as the UI thread), RPC_E_CHANGED_MODE
	// leaves that apartment intact; only S_OK and S_FALSE are paired with
	// CoUninitialize so we never leave a stray apartment on a Go worker thread.
	const coinitMultithreaded = 0
	if hr, _, _ := mfAPI.initCOM.Call(0, coinitMultithreaded); hr == 0 || hr == 1 {
		defer mfAPI.uninitCOM.Call()
	}

	e := &mediaEngine{shown: -1}
	if err := e.open(s); err != nil {
		e.release()
		return nil, err
	}
	return e, nil
}

// open makes the device and the engine and gives the engine its source.
func (e *mediaEngine) open(s Source) error {
	var err error
	if e.device, e.context, err = newVideoDevice(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	// The engine decodes on the device from threads of its own.
	if !hresultFailed(invoke(e.device, comQueryInterface, uintptr(unsafe.Pointer(&iidMultithread)), uintptr(unsafe.Pointer(&e.protected)))) {
		invoke(e.protected, multithreadSetProtected, 1)
	}
	var token uint32
	if hr, _, _ := mfAPI.deviceManager.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&e.manager))); hresultFailed(hr) {
		return fmt.Errorf("video: MFCreateDXGIDeviceManager: %#x", uint32(hr))
	}
	if hr := invoke(e.manager, managerReset, e.device, uintptr(token)); hresultFailed(hr) {
		return fmt.Errorf("video: IMFDXGIDeviceManager.ResetDevice: %#x", uint32(hr))
	}
	if e.notify = newNotify(e); e.notify == 0 {
		return errors.New("video: out of memory")
	}

	var attrs, factory uintptr
	if hr, _, _ := mfAPI.attributes.Call(uintptr(unsafe.Pointer(&attrs)), 3); hresultFailed(hr) {
		return fmt.Errorf("video: MFCreateAttributes: %#x", uint32(hr))
	}
	defer releaseCom(&attrs)
	invoke(attrs, attrSetUnknown, uintptr(unsafe.Pointer(&mfEngineCallback)), e.notify)
	invoke(attrs, attrSetUnknown, uintptr(unsafe.Pointer(&mfEngineDXGI)), e.manager)
	const formatBGRA = 87 // DXGI_FORMAT_B8G8R8A8_UNORM
	invoke(attrs, attrSetUint32, uintptr(unsafe.Pointer(&mfEngineVideoType)), formatBGRA)
	const clsctxInprocServer = 1
	if hr, _, _ := mfAPI.createCOM.Call(uintptr(unsafe.Pointer(&clsidEngineFactory)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidEngineFactory)), uintptr(unsafe.Pointer(&factory))); hresultFailed(hr) {
		return fmt.Errorf("%w: no media engine: %#x", ErrUnsupported, uint32(hr))
	}
	defer releaseCom(&factory)
	if hr := invoke(factory, factoryCreate, 0, attrs, uintptr(unsafe.Pointer(&e.engine))); hresultFailed(hr) {
		return fmt.Errorf("video: IMFMediaEngineClassFactory.CreateInstance: %#x", uint32(hr))
	}
	invoke(e.engine, comQueryInterface, uintptr(unsafe.Pointer(&iidEngineEx)), uintptr(unsafe.Pointer(&e.extended)))

	if s.Data != nil {
		return e.openData(s.Data)
	}
	// Media Foundation reads the percent-escapes of a file: URL as bytes of
	// the system's code page, not UTF-8, so a file whose path is not ASCII
	// goes by its path, which SetSource takes as well.
	src := s.URL
	if s.Path != "" {
		src = s.Path
	}
	url := allocBSTR(src)
	hr := invoke(e.engine, engineSetSource, url)
	mfAPI.freeString.Call(url)
	if hresultFailed(hr) {
		return fmt.Errorf("video: cannot open %s: %#x", src, uint32(hr))
	}
	return nil
}

// newNotify makes the IMFMediaEngineNotify object of e, in memory COM
// allocates, with an initial reference count of 1 owned by e, and registers e
// for the callbacks it takes.
func newNotify(e *mediaEngine) uintptr {
	mem, _, _ := mfAPI.alloc.Call(unsafe.Sizeof(notifyObject{}))
	if mem == 0 {
		return 0
	}
	obj := (*notifyObject)(ptrAt(mem))
	obj.vtbl = uintptr(unsafe.Pointer(&notifyMethods))
	obj.refs = 1
	liveEngines.Lock()
	liveEngines.byNotify[mem] = e
	liveEngines.Unlock()
	return mem
}

// openData has the engine read the video from bytes in memory, through a
// stream over those bytes, so the video needs no file.
func (e *mediaEngine) openData(data []byte) error {
	if e.extended == 0 {
		return fmt.Errorf("%w: no IMFMediaEngineEx", ErrUnsupported)
	}
	stream, _, _ := mfAPI.memoryStream.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if stream == 0 {
		return errors.New("video: out of memory")
	}
	defer releaseCom(&stream)
	var byteStream uintptr
	if hr, _, _ := mfAPI.streamOver.Call(stream, uintptr(unsafe.Pointer(&byteStream))); hresultFailed(hr) {
		return fmt.Errorf("video: MFCreateMFByteStreamOnStream: %#x", uint32(hr))
	}
	defer releaseCom(&byteStream)
	// The URL names only the container, for the engine to choose a source.
	name := "data.mp4"
	switch {
	case len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "qt  ":
		name = "data.mov"
	case len(data) >= 4 && string(data[:4]) == "\x1a\x45\xdf\xa3":
		name = "data.webm"
	}
	url := allocBSTR(name)
	defer mfAPI.freeString.Call(url)
	if hr := invoke(e.extended, engineExSourceFromMem, byteStream, url); hresultFailed(hr) {
		return fmt.Errorf("video: cannot open the video: %#x", uint32(hr))
	}
	return nil
}

// allocBSTR copies s into a BSTR, which the caller frees with SysFreeString.
func allocBSTR(s string) uintptr {
	text, _ := syscall.UTF16PtrFromString(s)
	b, _, _ := mfAPI.allocString.Call(uintptr(unsafe.Pointer(text)))
	return b
}

// dispatch takes an event the engine reported, on a thread of Media
// Foundation's.
func (e *mediaEngine) dispatch(event uint32, param1 uintptr, param2 uint32) {
	e.mu.Lock()
	switch event {
	case eventError:
		e.state = Failed
		e.failure = fmt.Errorf("video: Media Foundation could not play the video (error %d, %#x)", param1, param2)
	case eventEnded:
		if !e.repeat {
			e.state = Ended
		}
	case eventWaiting:
		if e.state == Ready {
			e.state = Buffering
		}
	case eventPlaying, eventCanPlay:
		if e.state == Buffering {
			e.state = Ready
		}
	case eventSeeked:
		e.seekStart = time.Time{}
		e.landed = true
	case eventFrameStepDone:
		e.stepDone = true
	case eventLoadedData:
		e.firstLoaded = true
	default:
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	e.wakeWindow()
}

// wakeWindow asks the window to paint a frame.
func (e *mediaEngine) wakeWindow() {
	e.mu.Lock()
	wake := e.wake
	e.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (e *mediaEngine) SetWake(wake func()) {
	e.mu.Lock()
	e.wake = wake
	e.mu.Unlock()
}

func (e *mediaEngine) Frame() (Picture, bool, bool) {
	if e.engine == 0 {
		return Picture{}, false, false
	}
	var pts int64
	fresh := invoke(e.engine, engineVideoStreamTick, uintptr(unsafe.Pointer(&pts))) == 0 // S_OK: a new picture
	e.mu.Lock()
	landed, target, stepDone := e.landed, e.seekTarget, e.stepDone
	e.mu.Unlock()
	if landed && (fresh || invoke(e.engine, engineIsSeeking) == 0) {
		e.mu.Lock()
		e.landed = false
		e.mu.Unlock()
		if !fresh && !e.pictured && e.extended != 0 && invoke(e.engine, engineIsPaused) != 0 {
			// A seek of a paused video can land without a picture, which then
			// never comes (Media Foundation on Windows on Arm, about one seek
			// in three). A step forward shows the picture there and keeps the
			// position. A seek just after a pause, as the video starts, may
			// also land where the video was (about one in ten), and seeking
			// again at once lands there too: once the step is done Frame
			// seeks again, a few times, and shows no picture from where the
			// video was. This recovery requires IMFMediaEngineEx; when
			// QueryInterface for it did not succeed, e.extended is 0 and the
			// workaround is skipped.
			if at := callDouble(e.engine, engineCurrentTime); e.retries < seekRetries && math.Abs(at-target.Seconds()) > 0.001 {
				e.reseek = true
				e.retries++
			}
			e.mu.Lock()
			e.stepDone, stepDone = false, false
			e.mu.Unlock()
			invoke(e.extended, engineExFrameStep, 1)
		}
	}
	changed := false
	if e.reseek {
		if stepDone {
			e.reseek = false
			e.seek(target)
		}
	} else if fresh {
		e.pictured = true
		var w, h uint32
		invoke(e.engine, engineNativeSize, uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&h)))
		if w > 0 && h > 0 {
			next := (e.shown + 1) % frameSlots
			if e.transfer(next, int(w), int(h)) {
				e.shown, changed = next, true
				e.width, e.height = int(w), int(h)
				e.mu.Lock()
				if e.state == Loading {
					e.state = Ready
				}
				e.mu.Unlock()
			}
		}
	}
	if e.shown < 0 {
		e.pollFirst()
		return Picture{}, false, false
	}
	slot := &e.slots[e.shown]
	return Picture{Handle: slot.handle, ID: slot.id, W: slot.width, H: slot.height}, changed, true
}

// pollFirst wakes the window again soon while the first picture is loaded
// but not there yet. No event says that a picture came, and the last one may
// come before it does: where decoding is slow (Windows 11 on Arm in a virtual
// machine without a GPU, even for H.264), Playing comes, the frame that wake
// asks for finds no picture (OnVideoStreamTick answers S_FALSE), and no event
// that wakes comes again until the end, so a window that paints only when
// woken shows nothing. Until the first picture, Frame asks for another look
// at the engine's interval, as Media Foundation's own samples look every
// vsync.
func (e *mediaEngine) pollFirst() {
	e.mu.Lock()
	due := !e.closed && !e.pollPending && e.firstLoaded && e.state == Loading && e.wake != nil
	e.mu.Unlock()
	// A video of sound alone has no picture to wait for.
	if !due || e.engine == 0 || invoke(e.engine, engineHasVideo) == 0 {
		return
	}
	e.mu.Lock()
	if e.closed || e.pollPending {
		e.mu.Unlock()
		return
	}
	e.pollPending = true
	e.mu.Unlock()
	time.AfterFunc(e.Interval(), func() {
		e.mu.Lock()
		closed := e.closed
		e.pollPending = false
		e.mu.Unlock()
		if !closed {
			e.wakeWindow()
		}
	})
}

// transfer converts the picture due into slot i, making the slot again when
// its size changed, and writing it under the slot's keyed mutex.
func (e *mediaEngine) transfer(i, w, h int) bool {
	slot := &e.slots[i]
	if slot.tex == 0 || slot.width != w || slot.height != h {
		e.freeSlot(slot)
		if !e.makeSlot(slot, w, h) {
			return false
		}
	}
	const wait = 100 // ms: the renderer reads the texture meanwhile
	// On WAIT_ABANDONED (0x80) we still own the mutex; overwriting the entire
	// texture and releasing restores valid contents.
	if hr := invoke(slot.mutex, keyedMutexAcquire, 0, wait); hresultFailed(hr) || hr == waitTimeout {
		return false
	}
	bounds := [4]int32{0, 0, int32(w), int32(h)}
	border := uint32(0xff000000) // opaque black, MFARGB
	hr := invoke(e.engine, engineTransferFrame, slot.tex, 0, uintptr(unsafe.Pointer(&bounds)), uintptr(unsafe.Pointer(&border)))
	invoke(slot.mutex, keyedMutexRelease, 0)
	return !hresultFailed(hr)
}

// makeSlot gives slot a w×h BGRA texture that the engine converts into and
// the renderer opens by an NT handle, ordered by a keyed mutex.
func (e *mediaEngine) makeSlot(slot *frameSlot, w, h int) bool {
	const (
		formatBGRA     = 87
		usageDefault   = 0
		bindShader     = 0x8
		bindRender     = 0x20
		sharedKeyed    = 0x100
		sharedNTHandle = 0x800
		readWrite      = 0x80000001 // GENERIC_READ | GENERIC_WRITE
	)
	desc := texture2DDesc{
		Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
		Format: formatBGRA, SampleCount: 1, Usage: usageDefault,
		BindFlags: bindShader | bindRender, MiscFlags: sharedKeyed | sharedNTHandle,
	}
	if hresultFailed(invoke(e.device, deviceCreateTexture, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&slot.tex)))) {
		slot.tex = 0
		return false
	}
	var resource uintptr
	if hresultFailed(invoke(slot.tex, comQueryInterface, uintptr(unsafe.Pointer(&iidKeyedMutex)), uintptr(unsafe.Pointer(&slot.mutex)))) ||
		hresultFailed(invoke(slot.tex, comQueryInterface, uintptr(unsafe.Pointer(&iidResource1)), uintptr(unsafe.Pointer(&resource)))) {
		e.freeSlot(slot)
		return false
	}
	hr := invoke(resource, resourceSharedHandle, 0, readWrite, 0, uintptr(unsafe.Pointer(&slot.handle)))
	releaseCom(&resource)
	if hresultFailed(hr) {
		e.freeSlot(slot)
		return false
	}
	slot.width, slot.height = w, h
	slot.id = sharedIDs.Add(1)
	registerSharedTexture(slot.id, e)
	return true
}

func (e *mediaEngine) freeSlot(slot *frameSlot) {
	if slot.id != 0 {
		retireSharedTexture(slot.id)
		slot.id = 0
	}
	releaseCom(&slot.mutex)
	releaseCom(&slot.tex)
	if slot.handle != 0 {
		syscall.CloseHandle(syscall.Handle(slot.handle))
		slot.handle = 0
	}
}

func (e *mediaEngine) Interval() time.Duration { return time.Second / 60 }

func (e *mediaEngine) State() (State, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Ended, nil
	}
	return e.state, e.failure
}

func (e *mediaEngine) Size() (int, int) {
	if e.engine == 0 {
		return 0, 0
	}
	return e.width, e.height
}

func (e *mediaEngine) Duration() time.Duration {
	if e.engine == 0 {
		return 0
	}
	seconds := callDouble(e.engine, engineDuration)
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func (e *mediaEngine) Position() time.Duration {
	if e.engine == 0 {
		return 0
	}
	e.mu.Lock()
	if !e.seekStart.IsZero() {
		target := e.seekTarget
		e.mu.Unlock()
		return target
	}
	e.mu.Unlock()
	seconds := callDouble(e.engine, engineCurrentTime)
	if math.IsNaN(seconds) || seconds < 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func (e *mediaEngine) Play() {
	if e.engine == 0 {
		return
	}
	e.mu.Lock()
	if e.state == Ended {
		e.state = Ready
	}
	e.mu.Unlock()
	invoke(e.engine, enginePlay)
}

func (e *mediaEngine) Pause() {
	if e.engine == 0 {
		return
	}
	invoke(e.engine, enginePause)
}

// Playing reports whether the engine is not paused. A video at its end plays
// no more, which the player knows by its state.
func (e *mediaEngine) Playing() bool {
	if e.engine == 0 {
		return false
	}
	return invoke(e.engine, engineIsPaused) == 0
}

func (e *mediaEngine) Seek(d time.Duration) {
	if e.engine == 0 {
		return
	}
	e.reseek, e.retries = false, 0
	e.seek(d)
}

func (e *mediaEngine) seek(d time.Duration) {
	if e.engine == 0 {
		return
	}
	e.mu.Lock()
	if e.state == Ended {
		e.state = Ready
	}
	e.seekTarget, e.seekStart, e.landed = d, time.Now(), false
	e.mu.Unlock()
	e.pictured = false
	callWithDouble(e.engine, engineSetCurrentTime, d.Seconds())
}

func (e *mediaEngine) SetLoop(loop bool) {
	if e.engine == 0 {
		return
	}
	e.mu.Lock()
	e.repeat = loop
	e.mu.Unlock()
	invoke(e.engine, engineSetLoop, uintptrOf(loop))
}

func (e *mediaEngine) SetRate(rate float64) {
	if e.engine == 0 {
		return
	}
	callWithDouble(e.engine, engineDefaultRate, rate)
	callWithDouble(e.engine, enginePlaybackRate, rate)
}

func (e *mediaEngine) SetVolume(volume float64) {
	if e.engine == 0 {
		return
	}
	callWithDouble(e.engine, engineSetVolume, volume)
}

func (e *mediaEngine) SetMuted(muted bool) {
	if e.engine == 0 {
		return
	}
	invoke(e.engine, engineSetMuted, uintptrOf(muted))
}

// Close shuts the engine down and frees what it made.
func (e *mediaEngine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.wake = nil
	e.mu.Unlock()
	// Detach callbacks before Shutdown so asynchronous events during or after
	// teardown never dispatch to the closing engine.
	if e.notify != 0 {
		liveEngines.Lock()
		delete(liveEngines.byNotify, e.notify)
		liveEngines.Unlock()
	}
	if e.engine != 0 {
		invoke(e.engine, engineShutdown)
	}
	e.release()
	return nil
}

func (e *mediaEngine) release() {
	releaseCom(&e.extended)
	releaseCom(&e.engine)
	if e.notify != 0 {
		liveEngines.Lock()
		delete(liveEngines.byNotify, e.notify)
		liveEngines.Unlock()
		// Drop Go's reference; the COM object frees itself once Media
		// Foundation has also released any remaining references.
		releaseCom(&e.notify)
	}
	for i := range e.slots {
		e.freeSlot(&e.slots[i])
	}
	releaseCom(&e.manager)
	releaseCom(&e.protected)
	releaseCom(&e.context)
	releaseCom(&e.device)
}
