# fpsm

**A free, open-source Fraps alternative for Windows.** An FPS counter and a screenshot hotkey for
DirectX 9, 10, 11 and 12, OpenGL and Vulkan games, including newer games that Fraps does not work
with.

Fraps hooks old graphics APIs from inside the game, which is why it fails on many newer titles.
fpsm does not touch the game at all: it reads the present events that Windows itself publishes
through ETW (the same source PresentMon uses). The DXGI and D3D9 runtimes publish one per frame,
and the graphics kernel publishes one for every present whatever API made it, which is how
OpenGL, Vulkan and older games are seen. Nothing is injected, so there is nothing for anti-cheat
to object to.

## Features

- **Lightweight:** one ~4 MB exe, no installer, about 18 MB of RAM while running. It only counts
  frame events, so it adds no load to the game.
- **Open source:** MIT licensed, small readable Go code, no telemetry, no network access.
- **FPS overlay:** a yellow number in a corner of the game, updated twice a second.
- **Screenshots:** press a hotkey to save a PNG of the game. The FPS number is left out of the
  picture, and the number flashes green to confirm the shot.
- **Works with newer games:** DX9 to DX12, OpenGL and Vulkan, no injection into the game.
- **Tray only:** right-click the icon to pick a corner, change the screenshot key, or quit.

## Download

Get the latest `fpsm.exe` (or the zip) from the
[Releases page](https://github.com/rohit267/fpsm/releases/latest). Each release lists SHA-256
checksums. The exe is not code-signed, so Windows SmartScreen may warn on first run: click
"More info", then "Run anyway".

Releases are built by GitHub Actions from this repository when a version tag (like `v0.1.0`) is
pushed, so the exe matches the source.

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
- OpenGL, Vulkan and older APIs, through the graphics kernel's present events.
- Screenshots use the Windows Desktop Duplication API, with a plain GDI capture as fallback.
- Tested so far on Windows 11 with The Witcher 3 and GTA: San Andreas (DirectX), and a small
  OpenGL test program. Vulkan uses the same kernel path as OpenGL but has not been tried with a
  real Vulkan game yet; if you try one, run `--probe` (below) and open an issue with the result.
- Not supported yet: screenshots on HDR displays.
- True exclusive fullscreen can hide the overlay; borderless and modern fullscreen work.

## Build

Needs Go 1.22 or newer on Windows.

```
go build -trimpath -ldflags "-s -w -H=windowsgui" -o fpsm.exe .
```

Two small command-line helpers:

- `fpsm.exe --probe 8 out.txt` listens for 8 seconds and writes every process that presented
  frames, with its FPS, to `out.txt` (add `--raw` to also list the raw event ids, and
  `--raw <pid>` to also count one process's events separately). Useful for checking whether a
  game is detected.
- `fpsm.exe --shot out.png` saves a screenshot of the window in front and exits.

## Roadmap

- HDR screenshots.
- Testing with more Vulkan and OpenGL games.

## License

MIT. See [LICENSE](LICENSE).
