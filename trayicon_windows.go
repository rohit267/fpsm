package main

import (
	"image"
	"image/color"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The tray icon is a fixed "60" (a label, not a live reading): yellow digits with a thin dark
// outline, the largest size that fits, centred on the digits' ink rather than the font's line box.

const (
	iconSize     = 32
	iconMaxPx    = 30
	iconMinPx    = 10
	iconOutlineR = 1
)

var iconFonts = map[int]uintptr{} // pixel height -> HFONT; only touched from the overlay thread

func iconFont(px int) uintptr {
	if f, ok := iconFonts[px]; ok {
		return f
	}
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	f, _, _ := procCreateFontW.Call(uintptr(-px&0xFFFFFFFF), 0, 0, 0, 700, 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(face)))
	iconFonts[px] = f
	return f
}

// numberIcon returns an .ico for text; nil if GDI fails.
func numberIcon(text string) []byte {
	d, err := newDIB(iconSize, iconSize)
	if err != nil {
		return nil
	}
	defer d.free()
	procSetBkMode.Call(d.dc, 1) // TRANSPARENT
	procSetTextColor.Call(d.dc, 0x00FFFFFF)

	var tw, th int
	for px := iconMaxPx; px >= iconMinPx; px-- {
		procSelectObject.Call(d.dc, iconFont(px))
		sz := textSize(d.dc, text)
		tw, th = sz.cx, sz.cy
		if tw <= iconSize-2*iconOutlineR {
			break
		}
	}

	mask := func(x, y, rad int) []byte {
		clear(d.bits)
		for dy := -rad; dy <= rad; dy++ {
			for dx := -rad; dx <= rad; dx++ {
				if dx*dx+dy*dy <= rad*rad+1 {
					textOut(d.dc, text, x+dx, y+dy)
				}
			}
		}
		procGdiFlush.Call()
		m := make([]byte, iconSize*iconSize)
		for i := range m {
			m[i] = d.bits[i*4+1]
		}
		return m
	}

	// First pass finds where the digits' ink landed; second pass draws them centred.
	x, y := (iconSize-tw)/2, (iconSize-th)/2
	probe := mask(x, y, 0)
	left, right, top, bottom := iconSize, -1, iconSize, -1
	for i, v := range probe {
		if v > 32 {
			px, py := i%iconSize, i/iconSize
			left, right = min(left, px), max(right, px)
			top, bottom = min(top, py), max(bottom, py)
		}
	}
	if right >= 0 {
		x += (iconSize-(right-left+1))/2 - left
		y += (iconSize-(bottom-top+1))/2 - top
	}
	fill := mask(x, y, 0)
	outline := mask(x, y, iconOutlineR)

	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	for i := range fill {
		f := float64(fill[i]) / 255
		o := float64(outline[i]) / 255 * outlineAlpha
		a := f + (1-f)*o
		if a <= 0 {
			continue
		}
		c := uint8(math.Round(255 * f / a)) // yellow over black outline
		img.SetNRGBA(i%iconSize, i/iconSize, color.NRGBA{R: c, G: c, B: 0, A: uint8(math.Round(255 * a))})
	}
	return encodeICO(img)
}
