# Supported Media Players

Kari is a terminal frontend, not a media player. It queries streaming backends, resolves stream URLs, headers, and subtitles, then launches your local player to handle actual video decoding and playback.

---

## Player Capability Matrix

| Player | Platform | IPC Telemetry | Smart Chapter Skip | Stream Headers | Subtitle Sidecars | Resume Accuracy |
| --- | --- | :---: | :---: | :---: | :---: | --- |
| **[mpv](https://mpv.io)** | Linux, macOS, Windows, Android | Yes | Yes (seekbar & OSD) | Yes (`Referer`, `Origin`, cookies) | Yes | **Exact (to the second)** |
| **[IINA](https://iina.io)** | macOS | Yes | Yes (via mpv IPC) | Yes | Yes (`--sub-files=`) | **Exact (to the second)** |
| **[VLC](https://videolan.org)** | Linux, macOS, Windows | No | No | Limited | Yes | Approximate (`--start-time`) |
| **[MPV Android](https://play.google.com/store/apps/details?id=is.xyz.mpv)** | Android (Termux) | No | No | Yes (via config bridge) | Yes | Last played episode |
| **[MX Player](https://play.google.com/store/apps/details?id=com.mxtech.videoplayer.ad)** | Android (Termux) | No | No | Yes (via Intent extras) | Yes | Player-internal |

---

## Player Details

### mpv (Recommended)

mpv gives you the full Kari experience across Linux, macOS, Windows, and Android (Termux):

- **2-Way JSON IPC**: Connects via a local Unix socket (Linux/macOS) or named pipe (Windows, `\\.\pipe\kari-mpv-<pid>`). Kari reads playback progress in real time and automatically updates your watch history when you quit.
- **Smart Skip & Virtual Chapters**: Kari loads an ephemeral Lua script (`--script=...`) into mpv with timestamps from SkipDB, IntroDB, Anime-Skip, and AniSkip. You get seekbar chapter ticks (`Opening`, `Ending`, `Recap`, `Preview`) and an interactive on-screen prompt (`Press Enter to Skip`).
- **Stream Headers**: Passes required HTTP headers (`Referer`, `Origin`, custom cookies) via `--http-header-fields` so streams play without 403 Forbidden errors.
- **Pipe Fallback**: If a stream host requires custom TLS handshakes or transport quirks, Kari automatically pipes data via `curl | mpv`.
- **Flatpak Detection**: Auto-detects Flatpak mpv (`io.mpv.Mpv`) on Linux systems where native mpv isn't installed.
- **Warning Video Detection**: Rejects short fake CDN/Cloudflare warning clips (< 60s) and automatically rolls over to the next mirror.

### IINA (macOS)

IINA is a native macOS player built on `libmpv`.

- Communicates through the same mpv IPC socket protocol, retaining position tracking and resume history.
- Loads subtitles via `--sub-files=` to avoid known `libmpv` CLI option bugs.
- Strips format overrides (`--demuxer-lavf-format=`) when subtitle files are loaded to prevent demuxer format bleed.

### VLC

VLC works across Linux, macOS, and Windows.

- Launches with `--play-and-exit` and `--no-video-title-show`.
- Passes the saved resume timestamp via `--start-time=<seconds>`.
- Does not expose an IPC socket to Kari, so watch progress updates on clean exit rather than live polling.
- Auto-detects `/Applications/VLC.app` on macOS even if the `vlc` binary isn't in your `$PATH`.

### Android (Termux)

#### MPV Android

- Launched via Android Intents through `am start`.
- Because Android Intents cannot reliably pass custom HTTP headers or complex CLI arguments, Kari uses a bridge file:
  Add `include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf` to your `mpv.conf` in the MPV Android app settings (**Settings → Advanced → Edit mpv.conf**). Kari writes stream headers and subtitle paths there before launch.

#### MX Player

- Launched via Android Intents targeting `com.mxtech.videoplayer.ad` (or Pro).
- Passes the stream URL and headers bundle (`headers` string array) directly through Intent extras.

---

## Switching Players

- **In the TUI**: Press `Ctrl+P` anytime to cycle through players detected on your system.
- **In Settings**: Press `s`, go to the **Playback** tab, and change **Preferred player**.
- **Via Environment**: Set `KARI_PLAYER=mpv` (or `iina`, `vlc`).
