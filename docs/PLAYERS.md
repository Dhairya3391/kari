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

- **Prerequisites**: The `termux-am` package must be installed (provides the `termux-am` binary; `termux-api` also ships `termux-am-starter`). The bare `am` binary is blocked by SELinux on many newer Android versions, so `termux-am` — which runs in the Termux app's own uid — is preferred.
- **Storage**: Run `termux-setup-storage` to grant write access to `/storage/emulated/0/Android/media/`. Without this, Kari cannot write the playback config where MPV can read it.
- **Technique**: Android Intents cannot pass complex configuration strings (like custom HTTP headers) to mpv — mpv-android's intent only accepts `title`, `position`, and `subs` extras, nothing else. So Kari uses an **include-file injection bridge**:
- **Bridge**: mpv-android loads config exclusively from its own app-data directory (`/data/user/0/is.xyz.mpv/files/`), which Termux cannot write. The user adds ONE line to that config once (via the app: Settings → Advanced → Edit mpv.conf):

  ```sh
  include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf
  ```

- **Process**:
  1. Kari writes the fresh playback config (`Referer`, `Origin`, `User-Agent`, `Cookie` via `http-header-fields`, `force-media-title`, `start`, network tuning, and `sub-file`) to `.mpv.conf` (mirrored as `mpv.conf`) in the MPV media directory.
  2. Downloaded subtitles are copied to `sub.vtt` in that directory and attached via a `sub-file=` line (mpv-android's `subs` intent extra requires a `Parcelable[] Uri` array that `am` cannot build).
  3. Kari launches the app via `termux-am` / `am start`, passing the stream URL plus the `title` and `position` extras it does support.
  4. On startup, mpv-android reads its internal `mpv.conf`, which `include`s `.mpv.conf`, applying the injected headers/subtitle.
- **Cleanup**: The `.mpv.conf` file is rewritten on every launch, so fresh stream tokens and titles always get applied.

## 4. Player Selection

The system attempts to use:

1. The user's preferred player (via `KARI_PLAYER` env var).
2. The default for the platform (`mpv`).
3. Any available player in the registry.
