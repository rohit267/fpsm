package main

import (
	"errors"
	"fmt"
	"image"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Screenshots grab the frame Windows is actually displaying using the DXGI Desktop Duplication
// API (what OBS display capture uses), which sees DX11/DX12 games that plain GDI capture misses.
// If duplication is unavailable, plain GDI BitBlt is the fallback. The overlay window is excluded
// from capture (see overlay_windows.go) so the FPS number is not in the picture.

var (
	dxgiDLL                = windows.NewLazySystemDLL("dxgi.dll")
	d3d11DLL               = windows.NewLazySystemDLL("d3d11.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice  = d3d11DLL.NewProc("D3D11CreateDevice")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procBitBlt             = gdi32.NewProc("BitBlt")
)

var (
	iidFactory1  = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidOutput1   = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidTexture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

const (
	hrWaitTimeout = 0x887A0027 // DXGI_ERROR_WAIT_TIMEOUT

	fmtB8G8R8A8 = 87
	fmtR8G8B8A8 = 28

	srccopy    = 0x00CC0020
	captureBlt = 0x40000000
)

// com is a raw COM interface pointer; call invokes vtable slot idx.
type com uintptr

//go:uintptrescapes
func (c com) call(idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(ptrAt(uintptr(c)))
	fn := *(*uintptr)(ptrAt(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(c)}, args...)...)
	return r
}

func (c com) release() {
	if c != 0 {
		c.call(2)
	}
}

//go:uintptrescapes
func callHR(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	return hrErr(r)
}

func hrErr(r uintptr) error {
	if int32(r) < 0 {
		return fmt.Errorf("HRESULT 0x%08X", uint32(r))
	}
	return nil
}

type dxgiOutputDesc struct {
	DeviceName [32]uint16
	Desktop    rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type mappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// capturePicture grabs the picture area of window hwnd (its client area; the whole screen for
// fullscreen and borderless windows).
func capturePicture(hwnd uintptr) (*image.NRGBA, error) {
	bounds, mon := windowBounds(hwnd)
	if bounds.Right <= bounds.Left || bounds.Bottom <= bounds.Top {
		return nil, errors.New("window has no visible area")
	}
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	crop := rect{bounds.Left - mi.Monitor.Left, bounds.Top - mi.Monitor.Top, bounds.Right - mi.Monitor.Left, bounds.Bottom - mi.Monitor.Top}

	img, err := captureDuplication(mon, crop)
	if err == nil {
		return img, nil
	}
	img, err2 := captureGDI(bounds)
	if err2 != nil {
		return nil, fmt.Errorf("desktop duplication: %v; GDI fallback: %v", err, err2)
	}
	return img, nil
}

// captureDuplication grabs crop (relative to the monitor's top-left) from monitor mon.
func captureDuplication(mon uintptr, crop rect) (*image.NRGBA, error) {
	var factory com
	if err := callHR(procCreateDXGIFactory1, uintptr(unsafe.Pointer(&iidFactory1)), uintptr(unsafe.Pointer(&factory))); err != nil {
		return nil, fmt.Errorf("CreateDXGIFactory1: %w", err)
	}
	defer factory.release()

	// Find the adapter and output that drive this monitor: duplication only works on the GPU
	// the monitor is plugged into, which on laptops and iGPU+dGPU boxes is not always the default.
	var adapter, output com
	for i := 0; output == 0; i++ {
		var a com
		if factory.call(12, uintptr(i), uintptr(unsafe.Pointer(&a))) != 0 { // IDXGIFactory1::EnumAdapters1
			break
		}
		for j := 0; ; j++ {
			var o com
			if a.call(7, uintptr(j), uintptr(unsafe.Pointer(&o))) != 0 { // IDXGIAdapter::EnumOutputs
				break
			}
			var desc dxgiOutputDesc
			o.call(7, uintptr(unsafe.Pointer(&desc))) // IDXGIOutput::GetDesc
			if desc.Monitor == mon {
				adapter, output = a, o
				break
			}
			o.release()
		}
		if output == 0 {
			a.release()
		}
	}
	if output == 0 {
		return nil, errors.New("no DXGI output for the window's monitor")
	}
	defer adapter.release()
	defer output.release()

	var dev, ctx com
	if err := callHR(procD3D11CreateDevice, uintptr(adapter), 0, 0, 0, 0, 0, 7, uintptr(unsafe.Pointer(&dev)), 0, uintptr(unsafe.Pointer(&ctx))); err != nil {
		return nil, fmt.Errorf("D3D11CreateDevice: %w", err)
	}
	defer dev.release()
	defer ctx.release()

	var out1, dup com
	if err := hrErr(output.call(0, uintptr(unsafe.Pointer(&iidOutput1)), uintptr(unsafe.Pointer(&out1)))); err != nil {
		return nil, fmt.Errorf("IDXGIOutput1: %w", err)
	}
	defer out1.release()
	if err := hrErr(out1.call(22, uintptr(dev), uintptr(unsafe.Pointer(&dup)))); err != nil { // DuplicateOutput
		return nil, fmt.Errorf("DuplicateOutput: %w", err)
	}
	defer dup.release()

	var res com
	var info [64]byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		r := dup.call(8, 100, uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&res))) // AcquireNextFrame
		if r == 0 {
			break
		}
		if uint32(r) != hrWaitTimeout || time.Now().After(deadline) {
			return nil, fmt.Errorf("AcquireNextFrame: HRESULT 0x%08X", uint32(r))
		}
	}
	defer dup.call(14) // ReleaseFrame
	defer res.release()

	var tex com
	if err := hrErr(res.call(0, uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(unsafe.Pointer(&tex)))); err != nil {
		return nil, fmt.Errorf("frame texture: %w", err)
	}
	defer tex.release()
	var td texture2DDesc
	tex.call(10, uintptr(unsafe.Pointer(&td))) // ID3D11Texture2D::GetDesc
	if td.Format != fmtB8G8R8A8 && td.Format != fmtR8G8B8A8 {
		return nil, fmt.Errorf("unsupported desktop format %d (HDR is not supported yet)", td.Format)
	}

	sd := td
	sd.Usage, sd.BindFlags, sd.CPUAccessFlags, sd.MiscFlags = 3, 0, 0x20000, 0 // STAGING, CPU read
	var staging com
	if err := hrErr(dev.call(5, uintptr(unsafe.Pointer(&sd)), 0, uintptr(unsafe.Pointer(&staging)))); err != nil {
		return nil, fmt.Errorf("CreateTexture2D: %w", err)
	}
	defer staging.release()
	ctx.call(47, uintptr(staging), uintptr(tex)) // CopyResource
	var mapped mappedSubresource
	if err := hrErr(ctx.call(14, uintptr(staging), 0, 1, 0, uintptr(unsafe.Pointer(&mapped)))); err != nil { // Map(READ)
		return nil, fmt.Errorf("Map: %w", err)
	}
	defer ctx.call(15, uintptr(staging), 0) // Unmap

	x0, y0 := int(max(crop.Left, 0)), int(max(crop.Top, 0))
	x1, y1 := int(min(crop.Right, int32(td.Width))), int(min(crop.Bottom, int32(td.Height)))
	if x1 <= x0 || y1 <= y0 {
		return nil, errors.New("capture area is outside the monitor")
	}
	img := image.NewNRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	swap := td.Format == fmtB8G8R8A8
	for y := y0; y < y1; y++ {
		row := unsafe.Slice((*byte)(ptrAt(mapped.Data+uintptr(y)*uintptr(mapped.RowPitch)+uintptr(x0)*4)), (x1-x0)*4)
		dst := img.Pix[(y-y0)*img.Stride:]
		for i := 0; i < len(row); i += 4 {
			if swap {
				dst[i], dst[i+1], dst[i+2] = row[i+2], row[i+1], row[i]
			} else {
				dst[i], dst[i+1], dst[i+2] = row[i], row[i+1], row[i+2]
			}
			dst[i+3] = 255
		}
	}
	return img, nil
}

// captureGDI copies bounds (virtual-screen coordinates) from the screen with BitBlt.
func captureGDI(bounds rect) (*image.NRGBA, error) {
	w, h := int(bounds.Right-bounds.Left), int(bounds.Bottom-bounds.Top)
	d, err := newDIB(w, h)
	if err != nil {
		return nil, err
	}
	defer d.free()
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, errors.New("GetDC failed")
	}
	defer procReleaseDC.Call(0, screen)
	if ok, _, _ := procBitBlt.Call(d.dc, 0, 0, uintptr(w), uintptr(h), screen, uintptr(bounds.Left), uintptr(bounds.Top), srccopy|captureBlt); ok == 0 {
		return nil, errors.New("BitBlt failed")
	}
	procGdiFlush.Call()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = d.bits[i*4+2], d.bits[i*4+1], d.bits[i*4+0], 255
	}
	return img, nil
}
