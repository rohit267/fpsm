package main

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	shcore                 = windows.NewLazySystemDLL("shcore.dll")
	procRegisterClassEx    = user32.NewProc("RegisterClassExW")
	procCreateWindowEx     = user32.NewProc("CreateWindowExW")
	procDefWindowProc      = user32.NewProc("DefWindowProcW")
	procGetMessage         = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessage    = user32.NewProc("DispatchMessageW")
	procPostMessage        = user32.NewProc("PostMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procSetTimer           = user32.NewProc("SetTimer")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procUpdateLayered      = user32.NewProc("UpdateLayeredWindow")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procGetForegroundWnd   = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procClientToScreen     = user32.NewProc("ClientToScreen")
	procMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo     = user32.NewProc("GetMonitorInfoW")
	procSetDpiAwareness    = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForMonitor   = shcore.NewProc("GetDpiForMonitor")
	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procSetDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procMessageBeep        = user32.NewProc("MessageBeep")
)

const (
	wsPopup           = 0x80000000
	wsExTopmost       = 0x00000008
	wsExTransparent   = 0x00000020
	wsExToolWindow    = 0x00000080
	wsExLayered       = 0x00080000
	wsExNoActivate    = 0x08000000
	swHide            = 0
	swShowNoActivate  = 4
	swpTopmostFlags   = 0x0053 // NOSIZE | NOMOVE | NOACTIVATE | SHOWWINDOW
	hwndTopmost       = ^uintptr(0)
	wmDestroy         = 0x0002
	wmTimer           = 0x0113
	wmApp             = 0x8000
	wmClose           = 0x0010
	wmHotkey          = 0x0312
	wmSetHotkey       = wmApp + 1 // re-register the screenshot hotkey from config
	hotkeyID          = 1
	modNoRepeat       = 0x4000
	wdaExcludeCapture = 0x11                   // keep this window out of screenshots and screen recordings
	flashFor          = 700 * time.Millisecond // the number turns green this long after a screenshot
	monitorNearest    = 2
	tickMs            = 500 // how often the number is re-read and redrawn
	margin            = 10  // px at 96 dpi, from the picture's edge to the digits
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point struct{ X, Y int32 }
type size struct{ CX, CY int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}
type blendFunction struct{ Op, Flags, Alpha, Format byte }

// Shell and compositor processes that present constantly but are never the game.
var ignoredProcs = map[string]bool{
	"explorer.exe": true, "dwm.exe": true, "searchhost.exe": true, "startmenuexperiencehost.exe": true,
	"shellexperiencehost.exe": true, "textinputhost.exe": true, "applicationframehost.exe": true,
}

// overlay is the click-through, always-on-top window that shows the number in a corner of the
// foreground app's picture, like Fraps does. For a windowed app that is just under the title bar.
type overlay struct {
	stats    *statTable
	cfg      *config
	selfPID  uint32
	hwnd     atomic.Uintptr
	onStatus func(string) // tooltip text, called from the overlay thread

	flashUntil atomic.Int64 // UnixNano until which the number shows in the "screenshot taken" colour

	rend       *renderer
	shown      bool
	lastText   string
	lastRight  bool
	lastFlash  bool
	lastStatus string
	names      map[uint32]string
}

func newOverlay(stats *statTable, cfg *config, onStatus func(string)) *overlay {
	return &overlay{stats: stats, cfg: cfg, selfPID: windows.GetCurrentProcessId(), onStatus: onStatus, names: make(map[uint32]string)}
}

func setDPIAware() {
	procSetDpiAwareness.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
}

// run creates the window and pumps messages until Close. Call from its own goroutine.
func (o *overlay) run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	inst, _, _ := procGetModuleHndl.Call(0)
	class, _ := windows.UTF16PtrFromString("FpsmOverlay")
	wc := wndClassEx{Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: syscall.NewCallback(o.wndProc), Instance: inst, ClassName: class}
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, err := procCreateWindowEx.Call(
		wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(class)), wsPopup,
		0, 0, 1, 1, 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	o.hwnd.Store(hwnd)
	procSetDisplayAffinity.Call(hwnd, wdaExcludeCapture) // needs Windows 10 2004+; harmless otherwise
	o.applyHotkey(hwnd)
	procSetTimer.Call(hwnd, 1, tickMs, 0)

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	if o.rend != nil {
		o.rend.free()
	}
	return nil
}

// Refresh re-evaluates now instead of waiting for the next tick (menu changes).
func (o *overlay) Refresh() {
	if h := o.hwnd.Load(); h != 0 {
		procPostMessage.Call(h, wmApp, 0, 0)
	}
}

func (o *overlay) Close() {
	if h := o.hwnd.Load(); h != 0 {
		procPostMessage.Call(h, wmClose, 0, 0)
	}
}

// SetHotkey re-reads the screenshot key from config (after a tray menu change).
func (o *overlay) SetHotkey() {
	if h := o.hwnd.Load(); h != 0 {
		procPostMessage.Call(h, wmSetHotkey, 0, 0)
	}
}

func (o *overlay) applyHotkey(hwnd uintptr) {
	procUnregisterHotKey.Call(hwnd, hotkeyID)
	if vk := o.cfg.shotVK(); vk != 0 {
		if ok, _, _ := procRegisterHotKey.Call(hwnd, hotkeyID, modNoRepeat, uintptr(vk)); ok == 0 {
			o.setStatus("fpsm - screenshot key is taken by another program")
		}
	}
}

// takeShot saves a PNG of the foreground app's picture. The overlay thread only notes what to
// capture; the capture and the PNG encode run on their own goroutine.
func (o *overlay) takeShot() {
	fg, _, _ := procGetForegroundWnd.Call()
	if fg == 0 {
		return
	}
	var pid uint32
	procGetWindowThreadPID.Call(fg, uintptr(unsafe.Pointer(&pid)))
	app := o.procName(pid)
	wasShown := o.shown
	go func() {
		img, err := capturePicture(fg)
		if err == nil {
			_, err = savePNG(img, shotDir(), app)
		}
		switch {
		case err != nil:
			procMessageBeep.Call(0x10) // MB_ICONHAND
		case wasShown:
			o.flashUntil.Store(time.Now().Add(flashFor).UnixNano())
			o.Refresh()
		default:
			procMessageBeep.Call(0) // no number on screen to flash
		}
	}()
}

func (o *overlay) wndProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmTimer, wmApp:
		o.tick(hwnd)
		return 0
	case wmSetHotkey:
		o.applyHotkey(hwnd)
		return 0
	case wmHotkey:
		o.takeShot()
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, m, wp, lp)
	return r
}

func (o *overlay) hide(hwnd uintptr) {
	if o.shown {
		procShowWindow.Call(hwnd, swHide)
		o.shown = false
	}
}

func (o *overlay) setStatus(s string) {
	if s != o.lastStatus && o.onStatus != nil {
		o.lastStatus = s
		o.onStatus(s)
	}
}

func (o *overlay) tick(hwnd uintptr) {
	now := qpc()
	fg, _, _ := procGetForegroundWnd.Call()
	var pid uint32
	if fg != 0 {
		procGetWindowThreadPID.Call(fg, uintptr(unsafe.Pointer(&pid)))
	}
	name := ""
	if pid != 0 && pid != o.selfPID {
		name = o.procName(pid)
	}
	fps, ok := 0, false
	if name != "" && !ignoredProcs[strings.ToLower(name)] {
		fps, ok = o.stats.fps(pid, now)
	}
	if !ok {
		o.hide(hwnd)
		o.setStatus("fpsm - running")
		return
	}
	o.setStatus("fpsm - " + strconv.Itoa(fps) + " fps (" + name + ")")
	if !o.cfg.visible.Load() {
		o.hide(hwnd)
		return
	}

	corner := int(o.cfg.corner.Load())
	bounds, mon := windowBounds(fg)
	scale := monitorScale(mon)
	if o.rend == nil || o.rend.scale != scale {
		if o.rend != nil {
			o.rend.free()
		}
		r, err := newRenderer(scale)
		if err != nil {
			o.rend = nil
			return
		}
		o.rend = r
		o.lastText = ""
	}

	text := strconv.Itoa(min(fps, 9999))
	right := cornerIsRight(corner)
	flash := time.Now().UnixNano() < o.flashUntil.Load()
	if text != o.lastText || right != o.lastRight || flash != o.lastFlash {
		o.rend.render(text, right, flash)
		o.lastText, o.lastRight, o.lastFlash = text, right, flash
	}

	// The digits sit `margin` from the edge of the picture: 10 px, or 1% of its height if larger.
	// The window is pad px bigger than the digits on every side, so pull it in by that much.
	m := max(int32(float64(margin)*scale), (bounds.Bottom-bounds.Top)/100) - int32(o.rend.pad)
	w, h := int32(o.rend.w), int32(o.rend.h)
	pt := point{bounds.Left + m, bounds.Top + m}
	if cornerIsRight(corner) {
		pt.X = bounds.Right - m - w
	}
	if cornerIsBottom(corner) {
		pt.Y = bounds.Bottom - m - h
	}
	sz := size{w, h}
	src := point{}
	bf := blendFunction{Op: 0, Alpha: 255, Format: 1} // AC_SRC_OVER, AC_SRC_ALPHA
	procUpdateLayered.Call(hwnd, 0, uintptr(unsafe.Pointer(&pt)), uintptr(unsafe.Pointer(&sz)),
		o.rend.final.dc, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&bf)), 2)
	// Re-assert topmost: games and the shell can push other windows above us.
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpTopmostFlags)
	o.shown = true
}

// windowBounds is the client area of the app's window in screen coordinates: the picture itself,
// without the title bar and borders, so a windowed game's number sits just under the title bar.
// For fullscreen and borderless windows that is the whole screen. Clamped to its monitor.
func windowBounds(hwnd uintptr) (rect, uintptr) {
	mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorNearest)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	var r rect
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok != 0 {
		origin := point{0, 0}
		procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin)))
		r = rect{origin.X, origin.Y, origin.X + r.Right, origin.Y + r.Bottom}
	} else {
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	}
	r.Left, r.Top = max(r.Left, mi.Monitor.Left), max(r.Top, mi.Monitor.Top)
	r.Right, r.Bottom = min(r.Right, mi.Monitor.Right), min(r.Bottom, mi.Monitor.Bottom)
	return r, mon
}

func monitorScale(mon uintptr) float64 {
	var dx, dy uint32
	if hr, _, _ := procGetDpiForMonitor.Call(mon, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); hr != 0 || dx == 0 {
		return 1
	}
	return float64(dx) / 96
}

func (o *overlay) procName(pid uint32) string {
	if n, ok := o.names[pid]; ok {
		return n
	}
	name := procImageName(pid)
	if len(o.names) > 256 {
		clear(o.names)
	}
	o.names[pid] = name
	return name
}

// procImageName is the exe name of pid, or "" if it cannot be opened.
func procImageName(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 520)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
