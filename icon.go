package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// encodeICO wraps a square image (up to 255 px) as a one-image .ico with a PNG payload.
func encodeICO(img *image.NRGBA) []byte {
	size := img.Bounds().Dx()
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)

	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, 1}) // ICONDIR: reserved, type=icon, count=1
	ico.Write([]byte{byte(size), byte(size), 0, 0})             // width, height, colors, reserved
	binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})   // planes, bit depth
	binary.Write(&ico, binary.LittleEndian, uint32(pngBuf.Len()))
	binary.Write(&ico, binary.LittleEndian, uint32(22)) // image offset: 6 + 16
	ico.Write(pngBuf.Bytes())
	return ico.Bytes()
}
