# 狩り Kari

**Stream anime, movies, TV, cartoons and manga from your terminal. No browser tabs.**

![Stars](https://img.shields.io/github/stars/Dhairya3391/kari?style=flat-square)
![Release](https://img.shields.io/github/v/release/Dhairya3391/kari?style=flat-square)
![License](https://img.shields.io/github/license/Dhairya3391/kari?style=flat-square)
![Go](https://img.shields.io/github/go-mod/go-version/Dhairya3391/kari?style=flat-square)
![CI](https://img.shields.io/github/actions/workflow/status/Dhairya3391/kari/ci.yml?branch=main&style=flat-square)
[![The Terminal Index](https://cli.masoko.net/badge/kari.svg)](https://cli.masoko.net/tools/kari/)
[![Kari on FMHY](https://cdn.jsdelivr.net/gh/JMcrafter26/badges@main/src/assets/available/fmhy/cozy-minimal.svg)](https://fmhy.net/video#anime-streaming-apps)
[![Kari on the AUR](https://cdn.jsdelivr.net/gh/JMcrafter26/badges@main/src/assets/available/arch-user-repository/cozy-minimal.svg)](https://aur.archlinux.org/packages/kari-bin)

![kari demo](assets/demo.gif)

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

### Arch Linux (AUR)

```bash
yay -S kari-bin   # or: paru -S kari-bin
```

Package: [kari-bin on the AUR](https://aur.archlinux.org/packages/kari-bin).

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
| **Sub / Dub** | Toggle with `a`; the track sticks for following episodes, subtitles side-loaded automatically |
| **Downloads** | Single or batch via yt-dlp, 16-connection with aria2 |
| **Stremio add-ons** | Add compatible stream manifests and browse their catalogs in Stremio mode |
| **Discord presence** | Optional, local only |
| **Self-update** | `kari -u` |

Players: mpv, IINA, VLC, MPV Android, MX Player. Capability matrix in [docs/PLAYERS.md](docs/PLAYERS.md).
Providers per mode: [docs/PROVIDERS.md](docs/PROVIDERS.md).

To add an add-on, open Settings with `s`, choose **Add-ons**, then **Add Stremio addon manifest**. You can paste a bare addon URL (Kari appends `/manifest.json`) or the full manifest URL including its configuration query string. Kari fetches the HTTP(S) manifest and accepts any reachable HTTP addon; no playback probe is required. Addons that only expose YouTube, torrent, or external-app links are saved but produce no playable streams — Kari needs a direct HTTP stream URL.

In the Add-ons list, `Enter`/`r` refreshes an add-on's manifest, `e` rebinds its URL, and `d` removes it. Disabling an add-on in any mode disables it everywhere, including Stremio browsing and playback.

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
5. In your mpv Android client: **Settings → Advanced → Edit mpv.conf**, add:

   ```ini
   include=/storage/emulated/0/Android/media/<package>/.mpv.conf
   ```

   The default package is `is.xyz.mpv`. To use another client, set
   `KARI_MPV_ANDROID_PACKAGE` to its Android package name in your Termux
   environment, then use that same package in the include path. This passes
   stream headers and subtitles from Termux to MPV.

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
