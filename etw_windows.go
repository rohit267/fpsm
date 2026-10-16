package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Frame counting without injecting into the game: the DXGI and D3D9 runtimes emit an ETW event
// every time an app calls Present. Reading those is how PresentMon works, and it covers D3D9/10/11/12
// games (The Witcher 3 included) without hooks, so there is nothing for anti-cheat to object to
// and no API Fraps has to hook. OpenGL, Vulkan and older APIs do not go through those runtimes,
// but every present ends in the Windows graphics kernel, which emits its own event per present.

var (
	guidDXGI = windows.GUID{Data1: 0xCA11C036, Data2: 0x0102, Data3: 0x4A2D, Data4: [8]byte{0xA6, 0xAD, 0xF0, 0x3C, 0xFE, 0xD5, 0xD3, 0xC9}}
	guidD3D9 = windows.GUID{Data1: 0x783ACA0A, Data2: 0x790E, Data3: 0x4D7F, Data4: [8]byte{0x84, 0x51, 0xAA, 0x85, 0x05, 0x11, 0xC6, 0xB9}}

	guidDxgKrnl = windows.GUID{Data1: 0x802EC45A, Data2: 0x1E99, Data3: 0x4B83, Data4: [8]byte{0x99, 0x20, 0x87, 0xC9, 0x82, 0x77, 0xBA, 0x9D}}
)

type provider struct {
	guid      windows.GUID
	name      string
	src       int
	presentID uint16 // Present_Start
}

var providers = [...]provider{
	{guidDXGI, "DXGI", srcDXGI, 42},
	{guidD3D9, "D3D9", srcD3D9, 1},
	// Every present, whatever API made it, ends in the kernel: this is how OpenGL, Vulkan and older
	// APIs are seen. Event 184 (0xB8, Present_Info) fires once per swap, attributed to the app.
	{guidDxgKrnl, "DxgKrnl", srcKernel, 184},
}

const (
	sessionName = "FpsmETW"

	errAccessDenied  = 5
	errAlreadyExists = 183

	controlStop         = 1
	wnodeFlagTracedGUID = 0x00020000
	logRealTime         = 0x00000100
	logNoPerProcBuf     = 0x10000000
	traceModeRealTime   = 0x00000100
	traceModeEventRec   = 0x10000000
	enableProvider      = 1
	levelVerbose        = 5
	filterTypeEventID   = 0x80000200
	enableParamsV2      = 2
	hdrFlag32Bit        = 0x20
	dxgiPresentTest     = 1
)

var (
	advapi32           = windows.NewLazySystemDLL("advapi32.dll")
	procStartTrace     = advapi32.NewProc("StartTraceW")
	procControlTrace   = advapi32.NewProc("ControlTraceW")
	procEnableTraceEx2 = advapi32.NewProc("EnableTraceEx2")
	procOpenTrace      = advapi32.NewProc("OpenTraceW")
	procProcessTrace   = advapi32.NewProc("ProcessTrace")
	procCloseTrace     = advapi32.NewProc("CloseTrace")
)

type wnodeHeader struct {
	BufferSize        uint32
	ProviderId        uint32
	HistoricalContext uint64
	TimeStamp         int64
	Guid              windows.GUID
	ClientContext     uint32
	Flags             uint32
}

type eventTraceProperties struct {
	Wnode               wnodeHeader
	BufferSize          uint32 // KB
	MinimumBuffers      uint32
	MaximumBuffers      uint32
	MaximumFileSize     uint32
	LogFileMode         uint32
	FlushTimer          uint32
	EnableFlags         uint32
	AgeLimit            int32
	NumberOfBuffers     uint32
	FreeBuffers         uint32
	EventsLost          uint32
	BuffersWritten      uint32
	LogBuffersLost      uint32
	RealTimeBuffersLost uint32
	LoggerThreadId      uintptr
	LogFileNameOffset   uint32
	LoggerNameOffset    uint32
}

// propsBuf is the properties struct followed by room for the session name, as StartTrace wants.
type propsBuf struct {
	p    eventTraceProperties
	name [128]uint16
}

func newProps(raw bool) *propsBuf {
	b := &propsBuf{}
	b.p.Wnode.BufferSize = uint32(unsafe.Sizeof(*b))
	b.p.Wnode.Flags = wnodeFlagTracedGUID
	b.p.Wnode.ClientContext = 1 // QPC timestamps
	// Tiny buffers (~20 events each) get handed to us as soon as they fill, so a game at 30+ fps
	// is reported several times per 0.5s overlay tick; the 1s flush covers slower presenters.
	b.p.BufferSize = 2
	b.p.MinimumBuffers = 8
	b.p.MaximumBuffers = 256
	if raw { // probing unfiltered providers: a firehose, so use big buffers
		b.p.BufferSize = 64
		b.p.MaximumBuffers = 512
	}
	b.p.LogFileMode = logRealTime | logNoPerProcBuf
	b.p.FlushTimer = 1
	b.p.LoggerNameOffset = uint32(unsafe.Offsetof(b.name))
	return b
}

type eventFilterDescriptor struct {
	Ptr  uint64
	Size uint32
	Type uint32
}

type enableTraceParameters struct {
	Version          uint32
	EnableProperty   uint32
	ControlFlags     uint32
	SourceId         windows.GUID
	EnableFilterDesc *eventFilterDescriptor
	FilterDescCount  uint32
}

type eventTraceHeader struct {
	Size       uint16
	HeaderType uint8
	Marker     uint8
	Type       uint8
	Level      uint8
	Version    uint16
	ThreadId   uint32
	ProcessId  uint32
	TimeStamp  int64
	Guid       windows.GUID
	KernelTime uint32
	UserTime   uint32
}

type eventTrace struct {
	Header           eventTraceHeader
	InstanceId       uint32
	ParentInstanceId uint32
	ParentGuid       windows.GUID
	MofData          uintptr
	MofLength        uint32
	ClientContext    uint32
}

type traceLogfileHeader struct {
	BufferSize         uint32
	Version            uint32
	ProviderVersion    uint32
	NumberOfProcessors uint32
	EndTime            int64
	TimerResolution    uint32
	MaximumFileSize    uint32
	LogFileMode        uint32
	BuffersWritten     uint32
	LogInstanceGuid    windows.GUID
	LoggerName         *uint16
	LogFileName        *uint16
	TimeZone           windows.Timezoneinformation
	BootTime           int64
	PerfFreq           int64
	StartTime          int64
	ReservedFlags      uint32
	BuffersLost        uint32
}

type eventTraceLogfile struct {
	LogFileName         *uint16
	LoggerName          *uint16
	CurrentTime         int64
	BuffersRead         uint32
	ProcessTraceMode    uint32
	CurrentEvent        eventTrace
	LogfileHeader       traceLogfileHeader
	BufferCallback      uintptr
	BufferSize          uint32
	Filled              uint32
	EventsLost          uint32
	EventRecordCallback uintptr
	IsKernelTrace       uint32
	Context             uintptr
}

type eventRecord struct {
	Size              uint16
	HeaderType        uint16
	Flags             uint16
	EventProperty     uint16
	ThreadId          uint32
	ProcessId         uint32
	TimeStamp         int64
	ProviderId        windows.GUID
	Id                uint16
	Version           uint8
	Channel           uint8
	Level             uint8
	Opcode            uint8
	Task              uint16
	Keyword           uint64
	KernelTime        uint32
	UserTime          uint32
	ActivityId        windows.GUID
	BufferContext     uint32
	ExtendedDataCount uint16
	UserDataLength    uint16
	ExtendedData      uintptr
	UserData          uintptr
	UserContext       uintptr
}

// errAdminRequired: Windows only lets administrators and "Performance Log Users" start ETW sessions.
var errAdminRequired = errors.New("access denied starting the ETW session: run as administrator")

type rawKey struct {
	prov string
	id   uint16
}

type etw struct {
	stats   *statTable
	name    *uint16
	props   *propsBuf
	logfile eventTraceLogfile
	session uint64
	trace   uint64
	done    chan struct{}

	// Kept on the heap: the OS reads these through raw pointers while enabling providers.
	filterIDs  [len(providers)][6]byte
	filterDesc eventFilterDescriptor
	params     enableTraceParameters

	rawMu  sync.Mutex
	raw    map[rawKey]int // event id histogram, only filled in probe --raw mode
	rawPID uint32         // if set, the histogram also counts this process's events separately
}

// startETW opens a real-time ETW session and feeds every Present event into stats. Needs admin.
// Delivery is limited to the Present_Start event ids; raw=true turns that filter off and
// records which event ids show up (for checking the ids on a new Windows build).
func startETW(stats *statTable, raw bool, rawPID uint32) (*etw, error) {
	e := &etw{stats: stats, done: make(chan struct{}), rawPID: rawPID}
	if raw {
		e.raw = make(map[rawKey]int)
	}
	e.name, _ = windows.UTF16PtrFromString(sessionName)

	for attempt := 0; ; attempt++ {
		e.props = newProps(raw)
		r, _, _ := procStartTrace.Call(uintptr(unsafe.Pointer(&e.session)), uintptr(unsafe.Pointer(e.name)), uintptr(unsafe.Pointer(e.props)))
		if r == 0 {
			break
		}
		if r == errAlreadyExists && attempt == 0 { // left behind by a crashed run
			stop := newProps(false)
			procControlTrace.Call(0, uintptr(unsafe.Pointer(e.name)), uintptr(unsafe.Pointer(stop)), controlStop)
			continue
		}
		if r == errAccessDenied {
			return nil, errAdminRequired
		}
		return nil, fmt.Errorf("StartTrace failed: %w", syscall.Errno(r))
	}

	for i, p := range providers {
		if p.presentID == 0 && !raw { // no known present event for this provider yet
			continue
		}
		err := e.enable(i, p, !raw)
		if err != nil && !raw { // event-id filtering is optional: fall back to every event
			err = e.enable(i, p, false)
		}
		if err != nil {
			e.stopSession()
			return nil, fmt.Errorf("enable %s: %w", p.name, err)
		}
	}

	e.logfile.LoggerName = e.name
	e.logfile.ProcessTraceMode = traceModeRealTime | traceModeEventRec
	e.logfile.EventRecordCallback = syscall.NewCallback(e.onEvent)
	h, _, err := procOpenTrace.Call(uintptr(unsafe.Pointer(&e.logfile)))
	if h == ^uintptr(0) {
		e.stopSession()
		return nil, fmt.Errorf("OpenTrace failed: %w", err)
	}
	e.trace = uint64(h)

	go func() {
		defer close(e.done)
		procProcessTrace.Call(uintptr(unsafe.Pointer(&e.trace)), 1, 0, 0)
	}()
	return e, nil
}

func (e *etw) enable(i int, p provider, filter bool) error {
	e.params = enableTraceParameters{Version: enableParamsV2}
	if filter {
		// EVENT_FILTER_EVENT_ID: FilterIn, Reserved, Count(u16), Events[]
		ids := &e.filterIDs[i]
		*ids = [6]byte{1, 0, 1, 0, byte(p.presentID), byte(p.presentID >> 8)}
		e.filterDesc = eventFilterDescriptor{Ptr: uint64(uintptr(unsafe.Pointer(&ids[0]))), Size: uint32(len(ids)), Type: filterTypeEventID}
		e.params.EnableFilterDesc = &e.filterDesc
		e.params.FilterDescCount = 1
	}
	r, _, _ := procEnableTraceEx2.Call(
		uintptr(e.session), uintptr(unsafe.Pointer(&p.guid)), enableProvider, levelVerbose,
		^uintptr(0), 0, 0, uintptr(unsafe.Pointer(&e.params)))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func ptrAt(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// onEvent is called by ProcessTrace for every event, on the ETW thread.
func (e *etw) onEvent(rec *eventRecord) uintptr {
	for i := range providers {
		p := &providers[i]
		if rec.ProviderId != p.guid {
			continue
		}
		if e.raw != nil {
			e.rawMu.Lock()
			e.raw[rawKey{p.name, rec.Id}]++
			if e.rawPID != 0 && e.rawPID == rec.ProcessId {
				e.raw[rawKey{p.name + " (target pid)", rec.Id}]++
			}
			e.rawMu.Unlock()
		}
		if rec.Id != p.presentID {
			return 0
		}
		if p.src == srcDXGI { // payload: pSwapChain, Flags, SyncInterval; skip DXGI_PRESENT_TEST
			off := uintptr(8)
			if rec.Flags&hdrFlag32Bit != 0 {
				off = 4
			}
			if rec.UserData != 0 && uintptr(rec.UserDataLength) >= off+4 {
				if *(*uint32)(ptrAt(rec.UserData + off))&dxgiPresentTest != 0 {
					return 0
				}
			}
		}
		e.stats.add(rec.ProcessId, p.src, rec.TimeStamp)
		return 0
	}
	return 0
}

func (e *etw) stopSession() {
	stop := newProps(false)
	procControlTrace.Call(0, uintptr(unsafe.Pointer(e.name)), uintptr(unsafe.Pointer(stop)), controlStop)
}

func (e *etw) Stop() {
	procCloseTrace.Call(uintptr(e.trace))
	e.stopSession()
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
	}
}

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procQPC           = kernel32.NewProc("QueryPerformanceCounter")
	procQPCFreq       = kernel32.NewProc("QueryPerformanceFrequency")
	procGetModuleHndl = kernel32.NewProc("GetModuleHandleW")
)

func qpc() int64 {
	var t int64
	procQPC.Call(uintptr(unsafe.Pointer(&t)))
	return t
}

func qpcFreq() int64 {
	var f int64
	procQPCFreq.Call(uintptr(unsafe.Pointer(&f)))
	return f
}
