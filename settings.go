package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Overlay corners, in menu order.
const (
	cornerTopLeft = iota
	cornerTopRight
	cornerBottomLeft
	cornerBottomRight
)

var cornerNames = [...]string{"Top left", "Top right", "Bottom left", "Bottom right"}

func cornerIsRight(c int) bool  { return c == cornerTopRight || c == cornerBottomRight }
func cornerIsBottom(c int) bool { return c == cornerBottomLeft || c == cornerBottomRight }

type savedSettings struct {
	Corner  int  `json:"corner"`
	Hidden  bool `json:"hidden"`
	ShotKey int  `json:"shotKey"` // index into shotKeys; 0 (F10) when absent
}

// config is read by the overlay thread and written by the tray menu.
type config struct {
	path    string
	corner  atomic.Int32
	visible atomic.Bool
	shotKey atomic.Int32
}

// shotVK is the virtual-key code of the screenshot hotkey, 0 if it is off.
func (c *config) shotVK() uint32 { return shotKeys[c.shotKey.Load()].vk }

func loadConfig() *config {
	c := &config{}
	c.visible.Store(true)
	dir, err := os.UserConfigDir()
	if err != nil {
		return c
	}
	c.path = filepath.Join(dir, "fpsm", "settings.json")
	if b, err := os.ReadFile(c.path); err == nil {
		var s savedSettings
		if json.Unmarshal(b, &s) == nil && s.Corner >= 0 && s.Corner < len(cornerNames) {
			c.corner.Store(int32(s.Corner))
			c.visible.Store(!s.Hidden)
			if s.ShotKey >= 0 && s.ShotKey < len(shotKeys) {
				c.shotKey.Store(int32(s.ShotKey))
			}
		}
	}
	return c
}

func (c *config) save() {
	if c.path == "" {
		return
	}
	b, _ := json.Marshal(savedSettings{Corner: int(c.corner.Load()), Hidden: !c.visible.Load(), ShotKey: int(c.shotKey.Load())})
	os.MkdirAll(filepath.Dir(c.path), 0o755)
	os.WriteFile(c.path, b, 0o644)
}
