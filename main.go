// fpsm: a free, open-source Fraps alternative for Windows. A frame rate overlay and a screenshot
// hotkey for DX9/10/11/12 games. It lives in the system tray; right-click the icon for the menu.
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

func main() {
	setDPIAware()
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "--dump" { // render a sample of the overlay to a PNG, no admin needed
		if err := dumpSample(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 2 && args[0] == "--shot" { // screenshot the foreground window to a PNG, then exit
		fg, _, _ := procGetForegroundWnd.Call()
		img, err := capturePicture(fg)
		if err == nil {
			var f *os.File
			if f, err = os.Create(args[1]); err == nil {
				err = png.Encode(f, img)
				f.Close()
			}
		}
		if err != nil {
			os.WriteFile(args[1]+".txt", []byte("error: "+err.Error()+"\n"), 0o644)
		}
		return
	}
	elevated := windows.GetCurrentProcessToken().IsElevated()
	if len(args) >= 3 && args[0] == "--probe" { // --probe <seconds> <out file> [--raw]
		if !elevated {
			relaunchElevated(args)
			return
		}
		runProbe(args[1:])
		return
	}
	mutexName, _ := windows.UTF16PtrFromString(`Global\fpsm`)
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err == windows.ERROR_ALREADY_EXISTS || err == windows.ERROR_ACCESS_DENIED {
		return // already running (possibly elevated); its icon is in the tray
	}

	cfg := loadConfig()
	stats := newStatTable(qpcFreq())
	ev, err := startETW(stats, false)
	if err == errAdminRequired && !elevated {
		// Not in "Performance Log Users": ask for admin once. Release the mutex so the elevated copy can take it.
		windows.CloseHandle(mutex)
		if err := relaunchElevated(args); err != nil {
			windows.MessageBox(0, ptr("fpsm needs administrator rights to read frame events.\n\n"+err.Error()), ptr("fpsm"), windows.MB_OK|windows.MB_ICONERROR)
		}
		return
	}
	if err != nil {
		windows.MessageBox(0, ptr(err.Error()), ptr("fpsm"), windows.MB_OK|windows.MB_ICONERROR)
		return
	}
	ov := newOverlay(stats, cfg, systray.SetTooltip)

	go func() {
		for range time.Tick(10 * time.Second) {
			stats.prune(qpc())
		}
	}()
	systray.Run(func() { onReady(cfg, ov) }, func() {
		ov.Close()
		ev.Stop()
	})
}

func onReady(cfg *config, ov *overlay) {
	systray.SetIcon(numberIcon("60"))
	systray.SetTitle("fpsm")
	systray.SetTooltip("fpsm - running")

	header := systray.AddMenuItem("fpsm - running", "")
	header.Disable()
	systray.AddSeparator()
	show := systray.AddMenuItemCheckbox("Show overlay", "", cfg.visible.Load())
	posMenu := systray.AddMenuItem("Position", "")
	corners := make([]*systray.MenuItem, len(cornerNames))
	for i, name := range cornerNames {
		corners[i] = posMenu.AddSubMenuItemCheckbox(name, "", i == int(cfg.corner.Load()))
	}
	keyMenu := systray.AddMenuItem("Screenshot key", "")
	keys := make([]*systray.MenuItem, len(shotKeys))
	for i, k := range shotKeys {
		keys[i] = keyMenu.AddSubMenuItemCheckbox(k.name, "", i == int(cfg.shotKey.Load()))
	}
	openShots := systray.AddMenuItem("Open screenshots folder", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Close fpsm")

	picks := make(chan int)
	for i, it := range corners {
		go func() {
			for range it.ClickedCh {
				picks <- i
			}
		}()
	}
	keyPicks := make(chan int)
	for i, it := range keys {
		go func() {
			for range it.ClickedCh {
				keyPicks <- i
			}
		}()
	}

	go func() {
		if err := ov.run(); err != nil {
			windows.MessageBox(0, ptr("Could not create the overlay window: "+err.Error()), ptr("fpsm"), windows.MB_OK|windows.MB_ICONERROR)
		}
	}()

	go func() {
		for {
			select {
			case <-show.ClickedCh:
				if show.Checked() {
					show.Uncheck()
				} else {
					show.Check()
				}
				cfg.visible.Store(show.Checked())
				cfg.save()
				ov.Refresh()
			case i := <-picks:
				for j, it := range corners {
					if j == i {
						it.Check()
					} else {
						it.Uncheck()
					}
				}
				cfg.corner.Store(int32(i))
				cfg.save()
				ov.Refresh()
			case i := <-keyPicks:
				for j, it := range keys {
					if j == i {
						it.Check()
					} else {
						it.Uncheck()
					}
				}
				cfg.shotKey.Store(int32(i))
				cfg.save()
				ov.SetHotkey()
			case <-openShots.ClickedCh:
				dir := shotDir()
				os.MkdirAll(dir, 0o755)
				windows.ShellExecute(0, ptr("open"), ptr(dir), nil, nil, windows.SW_SHOWNORMAL)
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func ptr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// relaunchElevated re-runs this exe behind a UAC prompt.
func relaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	return windows.ShellExecute(0, ptr("runas"), ptr(exe), ptr(strings.Join(quoted, " ")), ptr(filepath.Dir(exe)), windows.SW_NORMAL)
}

// runProbe listens for secs seconds, then writes every process that presented frames and its
// frame rate to a file. With --raw it also lists every event id the providers emitted.
func runProbe(args []string) {
	secs, _ := strconv.Atoi(args[0])
	out := args[1]
	raw := len(args) > 2 && args[2] == "--raw"
	var sb strings.Builder
	defer func() { os.WriteFile(out, []byte(sb.String()), 0o644) }()

	stats := newStatTable(qpcFreq())
	ev, err := startETW(stats, raw)
	if err != nil {
		fmt.Fprintf(&sb, "error: %v\n", err)
		return
	}
	time.Sleep(time.Duration(secs) * time.Second)
	active := stats.active(qpc())
	pids := make([]uint32, 0, len(active))
	for pid := range active {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return active[pids[i]] > active[pids[j]] })
	fmt.Fprintf(&sb, "processes presenting after %ds: %d\n", secs, len(pids))
	for _, pid := range pids {
		fmt.Fprintf(&sb, "  pid %-6d %-32s %4d fps\n", pid, procImageName(pid), active[pid])
	}
	if raw {
		ev.rawMu.Lock()
		keys := make([]rawKey, 0, len(ev.raw))
		for k := range ev.raw {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].prov+fmt.Sprint(keys[i].id) < keys[j].prov+fmt.Sprint(keys[j].id) })
		sb.WriteString("events seen (provider, id: count):\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "  %s id %d: %d\n", k.prov, k.id, ev.raw[k])
		}
		ev.rawMu.Unlock()
	}
	ev.Stop()
}

// dumpSample draws the overlay for a few values over a mid-grey backdrop and saves it as PNG.
func dumpSample(path string) error {
	r, err := newRenderer(1.5)
	if err != nil {
		return err
	}
	defer r.free()
	samples := []struct {
		text  string
		right bool
	}{{"144", false}, {"62", true}, {"9", false}}
	img := image.NewNRGBA(image.Rect(0, 0, r.w, r.h*len(samples)))
	bg := [3]float64{90, 110, 130}
	for si, s := range samples {
		px := r.render(s.text, s.right, false)
		for y := 0; y < r.h; y++ {
			for x := 0; x < r.w; x++ {
				i := (y*r.w + x) * 4
				a := float64(px[i+3]) / 255
				o := img.PixOffset(x, si*r.h+y)
				img.Pix[o+0] = uint8(float64(px[i+2]) + bg[0]*(1-a))
				img.Pix[o+1] = uint8(float64(px[i+1]) + bg[1]*(1-a))
				img.Pix[o+2] = uint8(float64(px[i+0]) + bg[2]*(1-a))
				img.Pix[o+3] = 255
			}
		}
	}
	if ico := numberIcon("60"); len(ico) > 22 { // the tray icon's PNG payload, for a look at it
		os.WriteFile(strings.TrimSuffix(path, filepath.Ext(path))+"-tray.png", ico[22:], 0o644)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
