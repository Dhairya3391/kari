# Media Players and Platform Support

Kari integrates with external media players to provide high-quality playback. 

## 1. Player Interface
While players use a struct-based registration, they effectively follow this behavioral interface:
- `Name()`: The unique identifier (e.g., `"mpv"`).
- `Available()`: Checks if the player's binary or app is installed on the host.
- `Play(sources, media)`: Handles the specific CLI flags or Intents needed to start playback.

## 2. Desktop (Linux / macOS)
On desktop platforms, we primarily use `exec.Command` to launch binaries.
- **MPV**: The default. Supports custom headers, referrers, and multiple subtitle files via CLI arguments.
- **IINA**: macOS-only wrapper for MPV.

## 3. Android (Termux)
Android requires a specialized "hack" to bypass Intent limitations.
- **Prerequisites**: The `termux-api` package must be installed (`pkg install termux-api`) to provide `termux-am-starter`. The bare `am` binary is blocked by SELinux on many newer Android versions.
- **Storage**: Run `termux-setup-storage` to grant write access to `/storage/emulated/0/Android/media/`. Without this, kari falls back to `~/.config/mpv/mpv.conf` (MPV Android may not read it).
- **Technique**: Since Android Intents cannot pass complex configuration strings (like custom headers) to every app, Kari uses an **Injection Directory**.
- **Location**: `/storage/emulated/0/Android/media/is.xyz.mpv/` (for MPV).
- **Process**:
  1. Kari writes configuration headers (`Referer`, `Origin`, `User-Agent`, `Cookie`, `force-media-title`) to all candidate config files (`.mpv.conf`, `mpv.conf`, `~/.config/mpv/mpv.conf`, `~/.mpv/mpv.conf`).
  2. Subtitles are copied to the MPV media directory as `sub.vtt`.
  3. Kari launches the app via `termux-am` or `am start`.
  4. The MPV Android app reads the configuration settings automatically on launch.
- **Cleanup**: Files are updated on every launch to ensure fresh stream tokens and titles.

## 4. Player Selection
The system attempts to use:
1. The user's preferred player (via `KARI_PLAYER` env var).
2. The default for the platform (`mpv`).
3. Any available player in the registry.
