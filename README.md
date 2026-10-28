<div align="center">

# 狩り Kari

**Stream anime, movies, TV, cartoons and manga from your terminal. No browser tabs.**

![Stars](https://img.shields.io/github/stars/Dhairya3391/kari?style=flat-square)
![Release](https://img.shields.io/github/v/release/Dhairya3391/kari?style=flat-square)
![License](https://img.shields.io/github/license/Dhairya3391/kari?style=flat-square)
![Go](https://img.shields.io/github/go-mod/go-version/Dhairya3391/kari?style=flat-square)
![CI](https://img.shields.io/github/actions/workflow/status/Dhairya3391/kari/ci.yml?branch=main&style=flat-square)
[![The Terminal Index](https://cli.masoko.net/badge/kari.svg)](https://cli.masoko.net/tools/kari/)

<img src="assets/demo.gif" width="800" alt="kari demo">

</div>

## Install

```bash
# macOS / Linux / Termux
curl -fsSL https://raw.githubusercontent.com/Dhairya3391/kari/main/install.sh | bash
```

```powershell
# Windows
irm https://raw.githubusercontent.com/Dhairya3391/kari/main/install.ps1 | iex
```

Or download a binary from [Releases](https://github.com/Dhairya3391/kari/releases/latest) (linux, macOS, windows, android; amd64/arm64).

**Requires a media player:** [mpv](https://mpv.io) (recommended), IINA (macOS) or VLC.
Kari is a terminal frontend, not a player. Android: see [Termux setup](#android-termux).

## Usage

```bash
kari            # open the TUI
kari "Frieren"  # search directly
kari -u         # self-update
kari -v         # version
```

Type a query, press `Space`, pick a result, press `Enter` to play. `Tab` switches mode.

## Features

| Feature | Description |
| --- | --- |
| **7 modes** | Anime, Movies, TV, Cartoons, Live/Sports, Manga, Jellyfin |
| **Parallel search** | Queries all providers at once, ranks streams by quality, language and server health |
| **Resume** | Tracks playback position through mpv/IINA IPC, resume from history |
| **Skip** | Auto-skip intros, outros and recaps, with seekbar chapter markers (mpv) |
| **Scrobbling** | AniList (anime), Trakt (movies/TV) |
| **Manga reader** | In-terminal, Kitty / iTerm2 / Sixel / Unicode block graphics |
| **Sub / Dub** | Toggle per title with `a`, subtitles side-loaded automatically |
| **Downloads** | Single or batch via yt-dlp, 16-connection with aria2 |
| **Discord presence** | Optional, local only |
| **Self-update** | `kari -u` |

Players: mpv, IINA, VLC, MPV Android, MX Player. Capability matrix in [docs/PLAYERS.md](docs/PLAYERS.md).
Providers per mode: [docs/PROVIDERS.md](docs/PROVIDERS.md).

## Essential keys

| Key | Action |
| --- | --- |
| `Space` | Focus search / run search |
| `Enter` | Select / play |
| `Tab` / `Shift+Tab` | Switch mode |
| `[` / `]` | Previous / next season (TV & Anime) |
| `j` / `k` or arrows | Move |
| `n` | Next episode |
| `h` | History |
| `s` | Settings |
| `?` | In-app keybinding help |
| `q` | Quit |

Everything else: [docs/KEYBINDINGS.md](docs/KEYBINDINGS.md)

## Configuration

Works with zero config. Change settings in-app (`s`), via `~/.config/kari/settings.json`, or env vars:

| Variable | Description |
| --- | --- |
| `KARI_PLAYER` | Force a player: `mpv`, `iina`, `vlc` |
| `KARI_DOWNLOAD_DIR` | Download destination |
| `KARI_IMG_PROTOCOL` | `kitty`, `iterm`, `sixel`, `blocks`, `none` |
| `JELLYFIN_URL` / `JELLYFIN_API_KEY` | Enable Jellyfin mode |

Full reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md)

**Optional, for downloads:** `yt-dlp` and `aria2`

```bash
brew install yt-dlp aria2          # macOS
sudo apt install yt-dlp aria2      # Debian/Ubuntu
winget install yt-dlp aria2        # Windows
```

## Platform notes

### Android (Termux)

1. Install Termux from [F-Droid](https://f-droid.org/packages/com.termux/) (not Google Play).
2. Set up packages and storage:

   ```bash
   pkg install curl termux-api yt-dlp aria2
   termux-setup-storage
   ```

3. Install [MPV Android](https://play.google.com/store/apps/details?id=is.xyz.mpv).
4. Install Kari with the install script above.
5. In MPV Android: **Settings → Advanced → Edit mpv.conf**, add:

   ```ini
   include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf
   ```

   This passes stream headers and subtitles from Termux to MPV.

### macOS Gatekeeper

If a downloaded binary is blocked:

```bash
xattr -d com.apple.quarantine ./kari
```

## Build from source

Requires Go 1.26+.

```bash
git clone https://github.com/Dhairya3391/kari.git
cd kari
./build.sh        # or: go build -o kari ./cmd/kari
./kari
```

## Contributing

PRs welcome. Adding a provider is the easiest way in.

- [CONTRIBUTING.md](CONTRIBUTING.md): workflow and pull request checklist
- [docs/CODEBASE.md](docs/CODEBASE.md): engineering standards
- [docs/PROVIDERS.md](docs/PROVIDERS.md): provider architecture
- [docs/EXTENDING.md](docs/EXTENDING.md): adding providers and players

Looking for something to pick up? Check [good first issues](https://github.com/Dhairya3391/kari/labels/good%20first%20issue).

## Disclaimer

Kari hosts, stores and distributes no media. It queries third-party sources and launches your local player. You are responsible for complying with copyright law where you live.

## License

[MIT](LICENSE)
