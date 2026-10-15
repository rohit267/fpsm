package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// shotKeys are the choices in the tray's "Screenshot key" menu; vk 0 means off.
var shotKeys = [...]struct {
	name string
	vk   uint32
}{
	{"F10", 0x79},
	{"F11", 0x7A},
	{"F12", 0x7B},
	{"Print Screen", 0x2C},
	{"Off", 0},
}

// shotDir is where screenshots go: Pictures\fpsm.
func shotDir() string {
	base, err := windows.KnownFolderPath(windows.FOLDERID_Pictures, 0)
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "Pictures")
	}
	return filepath.Join(base, "fpsm")
}

// savePNG writes img as "<app> <date time>.png" in dir and returns the path.
func savePNG(img image.Image, dir, app string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	app = strings.TrimSuffix(app, filepath.Ext(app))
	if app == "" {
		app = "screenshot"
	}
	stamp := time.Now().Format("2006-01-02 15-04-05")
	path := filepath.Join(dir, fmt.Sprintf("%s %s.png", app, stamp))
	for n := 2; ; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s %s (%d).png", app, stamp, n))
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	return path, f.Close()
}
