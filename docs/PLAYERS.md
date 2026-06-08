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
- **Technique**: Since Android Intents cannot pass complex configuration strings (like custom headers) to every app, we use an **Injection Directory**.
- **Location**: `/storage/emulated/0/android/media/is.xyz.mpv/` (for MPV).
- **Process**:
  1. Kari writes a temporary `mpv.conf` to this directory.
  2. Subtitles are copied to this same directory as `sub.vtt`.
  3. Kari launches the app via `am start`.
  4. The MPV Android app (if configured with `include=/.../mpv.conf`) loads these settings dynamically.
- **Cleanup**: Files are overwritten on every launch to ensure fresh settings.

## 4. Player Selection
The system attempts to use:
1. The user's preferred player (via `KARI_PLAYER` env var).
2. The default for the platform (`mpv`).
3. Any available player in the registry.
