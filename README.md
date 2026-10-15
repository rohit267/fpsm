# fpsm

**A free, open-source Fraps alternative for Windows.** An FPS counter and a screenshot hotkey for
DirectX 9, 10, 11 and 12 games, including newer games that Fraps does not work with.

Fraps hooks old graphics APIs from inside the game, which is why it fails on many newer titles.
fpsm does not touch the game at all: it reads the `Present` events that Windows' DXGI and D3D9
runtimes already publish through ETW (the same source PresentMon uses), so nothing is injected
and there is nothing for anti-cheat to object to.

## Features

- **Lightweight:** one ~4 MB exe, no installer, about 18 MB of RAM while running. It only counts
  frame events, so it adds no load to the game.
- **Open source:** MIT licensed, small readable Go code, no telemetry, no network access.
- **FPS overlay:** a yellow number in a corner of the game, updated twice a second.
- **Screenshots:** press a hotkey to save a PNG of the game. The FPS number is left out of the
  picture, and the number flashes green to confirm the shot.
- **Works with newer games:** DX9 to DX12, no injection into the game.
- **Tray only:** right-click the icon to pick a corner, change the screenshot key, or quit.

## Use

1. Run `fpsm.exe`. It asks for administrator rights once per launch (Windows only lets admins
   read frame events).
2. A yellow "60" icon appears in the tray. Start your game; the frame rate shows in a corner.
3. Press **F10** in the game to take a screenshot. Files go to `Pictures\fpsm`, named like
   `witcher3 2026-10-15 22-30-01.png`.
4. Right-click the tray icon for: Show overlay, Position (four corners), Screenshot key
   (F10, F11, F12, Print Screen or Off), Open screenshots folder, Quit.

Windowed games get the number in the corner of the picture, just under the title bar. Fullscreen
and borderless games get it in the screen corner.

## Supported

- DirectX 9, 10, 11 and 12 games (anything that presents through DXGI or D3D9).
- Screenshots use the Windows Desktop Duplication API, with a plain GDI capture as fallback.
- Tested so far on Windows 11 with The Witcher 3 and GTA: San Andreas.
- Not supported yet: Vulkan and OpenGL games, screenshots on HDR displays.
- True exclusive fullscreen can hide the overlay; borderless and modern fullscreen work.

## Build

Needs Go 1.22 or newer on Windows.

```
go build -trimpath -ldflags "-s -w -H=windowsgui" -o fpsm.exe .
```

Two small command-line helpers:

- `fpsm.exe --probe 8 out.txt` listens for 8 seconds and writes every process that presented
  frames, with its FPS, to `out.txt` (add `--raw` to also list the raw event ids). Useful for
  checking whether a game is detected.
- `fpsm.exe --shot out.png` saves a screenshot of the window in front and exits.

## Roadmap

- Vulkan and OpenGL support.
- HDR screenshots.

## License

MIT. See [LICENSE](LICENSE).
