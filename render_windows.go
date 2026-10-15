package main

import (
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Fraps-style number: yellow digits with a dark outline on a transparent background.
// GDI draws the glyphs (white on black, so the grey level is the anti-aliased coverage); that
// coverage is turned into premultiplied-alpha pixels for a layered window.

var (
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procCreateCompatDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procDeleteDC         = gdi32.NewProc("DeleteDC")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procTextOutW         = gdi32.NewProc("TextOutW")
	procGetTextExtent    = gdi32.NewProc("GetTextExtentPoint32W")
	procGdiFlush         = gdi32.NewProc("GdiFlush")
)

const (
	baseFontPx   = 34 // at 96 dpi
	outlineAlpha = 0.9
	sampleText   = "8888" // widest the number gets; sets the window size
)

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPels, YPels  int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type dib struct {
	dc, bmp, old uintptr
	bits         []byte // w*h*4, BGRA, top-down
}

func newDIB(w, h int) (*dib, error) {
	bi := bitmapInfoHeader{Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	dc, _, _ := procCreateCompatDC.Call(0)
	if dc == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	var bits unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		procDeleteDC.Call(dc)
		return nil, fmt.Errorf("CreateDIBSection failed: %w", err)
	}
	old, _, _ := procSelectObject.Call(dc, bmp)
	return &dib{dc: dc, bmp: bmp, old: old, bits: unsafe.Slice((*byte)(bits), w*h*4)}, nil
}

func (d *dib) free() {
	procSelectObject.Call(d.dc, d.old)
	procDeleteObject.Call(d.bmp)
	procDeleteDC.Call(d.dc)
}

type renderer struct {
	scale         float64
	font          uintptr
	w, h, pad     int
	outline       int // outline radius in px
	lb, rb        int // empty px between the text box and the digits' ink, left and right
	inkTop        int // empty px between the top of the text box and the digits' ink
	scratch       *dib
	final         *dib
	outMask, fill []byte
}

// inkBox draws s in font on a scratch bitmap and returns the extent of its lit pixels relative
// to the text origin, plus the text's own advance size.
func inkBox(font uintptr, s string) (left, top, right, bottom int, ext textExtent, err error) {
	m, err := newDIB(1, 1)
	if err != nil {
		return
	}
	procSelectObject.Call(m.dc, font)
	ext = textSize(m.dc, s)
	m.free()

	const pad = 8
	w, h := ext.cx+2*pad, ext.cy+2*pad
	d, err := newDIB(w, h)
	if err != nil {
		return
	}
	defer d.free()
	procSelectObject.Call(d.dc, font)
	procSetBkMode.Call(d.dc, 1) // TRANSPARENT
	procSetTextColor.Call(d.dc, 0x00FFFFFF)
	textOut(d.dc, s, pad, pad)
	procGdiFlush.Call()
	left, top, right, bottom = w, h, -1, -1
	for i := 0; i < w*h; i++ {
		if d.bits[i*4+1] > 32 {
			x, y := i%w, i/w
			left, right = min(left, x), max(right, x)
			top, bottom = min(top, y), max(bottom, y)
		}
	}
	return left - pad, top - pad, right - pad, bottom - pad, ext, nil
}

func newRenderer(scale float64) (*renderer, error) {
	r := &renderer{scale: scale}
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	px := int(math.Round(baseFontPx * scale))
	// CreateFontW(height, 0, 0, 0, FW_BOLD, 0, 0, 0, DEFAULT_CHARSET, 0, 0, ANTIALIASED_QUALITY, 0, face)
	f, _, _ := procCreateFontW.Call(uintptr(-px&0xFFFFFFFF), 0, 0, 0, 700, 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(face)))
	if f == 0 {
		return nil, fmt.Errorf("CreateFont failed")
	}
	r.font = f
	r.outline = int(math.Ceil(2 * scale))
	r.pad = r.outline + 1

	// Size the window to the digits' ink, not the font's line box, so the empty space the font
	// reserves above and beside the digits does not add to the margin from the screen edge.
	l, _, rt, _, ext, err := inkBox(f, sampleText)
	if err != nil {
		return nil, err
	}
	_, top, _, bottom, _, err := inkBox(f, "0123456789")
	if err != nil {
		return nil, err
	}
	r.lb, r.rb, r.inkTop = l, ext.cx-1-rt, top
	r.w = ext.cx - r.lb - r.rb + 2*r.pad
	r.h = bottom - top + 1 + 2*r.pad

	if r.scratch, err = newDIB(r.w, r.h); err != nil {
		return nil, err
	}
	if r.final, err = newDIB(r.w, r.h); err != nil {
		r.scratch.free()
		return nil, err
	}
	procSelectObject.Call(r.scratch.dc, f)
	procSetBkMode.Call(r.scratch.dc, 1) // TRANSPARENT
	procSetTextColor.Call(r.scratch.dc, 0x00FFFFFF)
	r.outMask = make([]byte, r.w*r.h)
	r.fill = make([]byte, r.w*r.h)
	return r, nil
}

func (r *renderer) free() {
	r.scratch.free()
	r.final.free()
	procDeleteObject.Call(r.font)
}

type textExtent struct{ cx, cy int }

func textSize(dc uintptr, s string) textExtent {
	u, _ := windows.UTF16FromString(s)
	var sz [2]int32
	procGetTextExtent.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&sz)))
	return textExtent{int(sz[0]), int(sz[1])}
}

func textOut(dc uintptr, s string, x, y int) {
	u, _ := windows.UTF16FromString(s)
	procTextOutW.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))
}

func (r *renderer) drawText(s string, x, y int) { textOut(r.scratch.dc, s, x, y) }

// grabMask copies the grey level (green channel) of the scratch bitmap into dst, then clears it.
func (r *renderer) grabMask(dst []byte) {
	procGdiFlush.Call()
	b := r.scratch.bits
	for i := range dst {
		dst[i] = b[i*4+1]
	}
	clear(b)
}

// render draws s into the final bitmap (premultiplied BGRA) and returns it. Digits hug the
// right edge for right-hand corners, the left edge otherwise.
func (r *renderer) render(s string, right, flash bool) []byte {
	clear(r.scratch.bits)
	tw := textSize(r.scratch.dc, s).cx
	x := r.pad - r.lb
	if right {
		x = r.w - r.pad - tw + r.rb
	}
	y := r.pad - r.inkTop

	rad := r.outline
	for dy := -rad; dy <= rad; dy++ {
		for dx := -rad; dx <= rad; dx++ {
			if dx*dx+dy*dy <= rad*rad+1 {
				r.drawText(s, x+dx, y+dy)
			}
		}
	}
	r.grabMask(r.outMask)
	r.drawText(s, x, y)
	r.grabMask(r.fill)

	// Yellow digits; green for a moment after a screenshot. Colour channels are premultiplied by the fill.
	cr, cg, cb := 255.0, 255.0, 0.0
	if flash {
		cr, cg, cb = 90, 255, 90
	}
	out := r.final.bits
	for i := range r.fill {
		f := float64(r.fill[i]) / 255
		o := float64(r.outMask[i]) / 255 * outlineAlpha
		a := f + (1-f)*o
		out[i*4+0] = uint8(math.Round(cb * f))
		out[i*4+1] = uint8(math.Round(cg * f))
		out[i*4+2] = uint8(math.Round(cr * f))
		out[i*4+3] = uint8(math.Round(255 * a))
	}
	return out
}
