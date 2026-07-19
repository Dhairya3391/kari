# Kari (狩り)

![Stars](https://img.shields.io/github/stars/Dhairya3391/kari?style=flat-square&label=stars)
![License](https://img.shields.io/github/license/Dhairya3391/kari?style=flat-square)
![Go Version](https://img.shields.io/github/go-mod/go-version/Dhairya3391/kari?style=flat-square&label=go)
![Last Commit](https://img.shields.io/github/last-commit/Dhairya3391/kari?style=flat-square)

I wanted MPV for playback, Trakt for tracking, AniList for anime, subtitles when I needed them, and exactly zero browser tabs.

So I built Kari.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

![kari demo](https://github.com/user-attachments/assets/be296568-f5c1-4675-86e4-626434f2829c)

## Quick Start

```bash
git clone https://github.com/Dhairya3391/kari.git
cd kari
go build -o kari ./cmd/kari
./kari
```

Type a query, press Space, pick a result.

## Features

- **Multi-source search** — Searches multiple providers at once (MovieBox, VidKing, Miruro, WCO, and optional Jellyfin). Results grouped by source.
- **5 media modes** — Anime, Movies, TV Shows, Cartoons, Jellyfin. Switch with Tab.
- **Episode browsing** — Season/episode lists with filler badges and sub/dub toggles.
- **Parallel source resolution** — Queries every provider at once, shows all available qualities.
- **External player support** — MPV (any OS), IINA (macOS), VLC (Windows), MX Player (Android).
- **IPC position tracking** — MPV reports back via Unix socket. Close mid-episode, resume where you left off.
- **Auto-skip intro/outro** — Pulls timestamps from AniSkip, generates an MPV Lua script on the fly.
- **Scrobbling** — Syncs watch status to Trakt.tv (movies/TV) via device auth (no tokens to copy) and AniList (anime) via OAuth.
- **Watch history** — Local JSON store, grouped by series, remembers your position.
- **Downloading** — Built-in HLS segment downloader (Miruro) and HTTP range-request downloader (WCO), with yt-dlp as fallback for everything else.
- **Subtitles** — Pulls from OpenSubtitles automatically, Yify as fallback.
- **Self-update** — `./kari -u` fetches the latest release from GitHub.
- **Cross-platform** — Linux, macOS, Windows, Android (Termux).

## Installation

### Prerequisites

- **Go 1.26+** — [Install Go](https://go.dev/doc/install)

At least one media player in `$PATH`:

| Player | Platforms | Notes |
| --- | --- | --- |
| [MPV](https://mpv.io/) | Linux, macOS, Windows, Android | Full support |
| [IINA](https://iina.io/) | macOS | MPV-based; position tracking works |
| [VLC](https://www.videolan.org/) | Windows | No position tracking |
| [MX Player](https://play.google.com/store/apps/details?id=com.mxtech.videoplayer.ad) | Android | Launched via `am start`; no position tracking |

Optional tools:

| Tool | Used for | In `$PATH` |
| --- | --- | --- |
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Downloading non-Miruro/WCO sources | Yes |
| [aria2c](https://aria2.github.io/) | Speeds up yt-dlp downloads | Yes |
| [curl](https://curl.se/) | WCO cookie bootstrap + MPV pipe playback | Yes |
| [upx](https://upx.github.io/) | Compressing build artifacts (build script only) | No |

### Build from source

```bash
git clone https://github.com/Dhairya3391/kari.git
cd kari
go build -o kari ./cmd/kari
```

The binary lands in the current directory as `./kari`.

### Pre-built binaries

If you don't want to install Go, grab a binary from the [releases page](https://github.com/Dhairya3391/kari/releases).

## Configuration

All config is through environment variables. No config files to wrangle.

### Essential

| Variable | Description | Default |
| --- | --- | --- |
| `KARI_PLAYER` | Preferred player (`mpv`, `iina`, `vlc`) | Auto-detected |

### API Keys (optional — built-in fallbacks are there if you don't set these)

| Variable | Description |
| --- | --- |
| `TMDB_API_KEY` | Your own TMDB key. If unset, a pool of built-in keys is rotated through. |
| `OPENSUBTITLES_API_KEY` | OpenSubtitles API key. If unset, falls back to Yify. |
| `OPENSUBTITLES_USERNAME` | Required if you set the API key. |
| `OPENSUBTITLES_PASSWORD` | Same. |
| `JELLYFIN_URL` | Your Jellyfin server URL (example: `"https://jfn.fqdn.com/"`). When set, a JELLYFIN mode appears. |
| `JELLYFIN_API_KEY` | Jellyfin API key. Required if `JELLYFIN_URL` is set. Generate one in Dashboard → API Keys. |
| `TRAKT_CLIENT_ID` / `TRAKT_ID` | Trakt.tv OAuth client ID |
| `TRAKT_CLIENT_SECRET` / `TRAKT_SECRET` | Trakt.tv OAuth client secret |
| `ANILIST_CLIENT_ID` / `ANILIST_ID` | AniList OAuth client ID |
| `ANILIST_CLIENT_SECRET` / `ANILIST_SECRET` | AniList OAuth client secret |

The binary ships with fallback keys for Trakt, AniList, and TMDB. They work fine for casual use. If you hit rate limits, set your own.

### Other

| Variable | Description | Default |
| --- | --- | --- |
| `KARI_DOWNLOAD_DIR` | Where downloaded episodes land | `./downloads` |
| `KARI_LOG_FILE` | Log file path | `~/.config/kari/kari.log` |
| `KARI_LOG_DEBUG` | Enable debug logging (`1`, `true`) | `false` |
| `KARI_LOG_STDERR` | Also write logs to stderr (`1`, `true`) | `false` |

## Usage

```bash
./kari                    # Opens the TUI. Start typing to search.
./kari "one piece"        # Skips straight to search results.
./kari -v --version       # Print version.
./kari -u --update        # Self-update from GitHub releases.
```

### Controls

#### Navigation

| Key | Action |
| --- | --- |
| `↑/↓` or `j/k` | Move through lists |
| `Enter` | Select series / episode / play |
| `Esc` | Go back |
| `/` | Filter list items |

#### Search & Browse

| Key | Action |
| --- | --- |
| `Space` | Start search |
| `Tab` / `Shift+Tab` | Cycle media mode (Anime → Movies → TV → Cartoon → Jellyfin) |
| `Ctrl+H` | Go to home/search |

#### Playback

| Key | Action |
| --- | --- |
| `n` | Play next episode |
| `A` | Toggle autoplay |
| `a` | Toggle sub/dub (anime only) |
| `r` | Restart episode from beginning |
| `Ctrl+P` | Switch players |

#### Downloads

| Key | Action |
| --- | --- |
| `d` | Download selected episode |
| `x` | Stop active download / cancel |

#### App

| Key | Action |
| --- | --- |
| `h` / `H` | Open watch history |
| `s` / `S` | Settings (Trakt/AniList auth) |
| `Ctrl+D` | Toggle debug panel |
| `q` / `Ctrl+C` | Quit |

### Views

1. **Search** — Type a query, hit Space. Results come back grouped by provider.
2. **Episodes** — Season/episode list with filler badges. Sub/dub toggle is there for anime.
3. **Preview** — Shows resolved sources, subtitles, and any saved position. Hit Enter to play.
4. **History** — Grouped by series. Resume or delete.
5. **Settings** — Authenticate with Trakt or AniList via device auth (no manual token fiddling).

### Media Providers

| Provider | Mode | Method | Priority |
| --- | --- | --- | --- |
| Miruro | Anime | API | 1 |
| MovieBox | Movies, TV | API (via TMDB) | 2 |
| VidKing | Movies, TV | API (via TMDB) | 2 |
| WCO | Cartoons, Anime | HTML scraping | 2 |
| Jellyfin | Movies, TV | Jellyfin API | 1 |

Lower priority = queried first. All providers are queried in parallel regardless — priority only affects result ordering when multiple providers return the same content.

Want to add another provider? See [docs/PROVIDERS.md](docs/PROVIDERS.md) and [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md).

## Android Setup (Termux)

Android support is a bit hacky (MPV Android doesn't expose a normal config directory), but it works:

1. Install [Termux](https://termux.dev/) from F-Droid (not Play Store — the Play Store version is abandoned)
2. Install dependencies:

   ```bash
   pkg install golang curl termux-api yt-dlp
   ```

   | Package | Why |
   |---------|-----|
   | `golang` | Build kari |
   | `curl` | Cookie bootstrap + pipe-to-mpv playback |
   | `termux-api` | Provides `termux-am-starter` to launch MPV/MX Player via Android intents (the bare `am` binary is broken on newer Android) |
   | `yt-dlp` | Required for downloads (optional — skip if you only stream) |

3. Grant storage access (needed to write mpv.conf to the MPV Android config dir):

   ```bash
   termux-setup-storage
   ```

   Then allow the storage permission when prompted. Without this, kari falls back to `~/.config/mpv/mpv.conf` (MPV Android may not read it).

4. Clone and build:

   ```bash
   git clone https://github.com/Dhairya3391/kari.git
   cd kari
   go build -o kari ./cmd/kari
   ```

5. Install [MPV Android](https://play.google.com/store/apps/details?id=is.xyz.mpv) from Play Store
6. Create a `mpv.conf` at `/storage/emulated/0/Android/media/is.xyz.mpv/mpv.conf`:

   ```ini
   include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf
   ```

7. Run `./kari`

Kari launches MPV via Android `am start` intents. The mpv.conf redirect lets it write playback scripts where they can actually be read. See [docs/PLAYERS.md](docs/PLAYERS.md) for the details of this hack.

> **Note:** On some Android versions, DNS resolution may fail for downloads. Kari includes a built-in fallback to Cloudflare (1.1.1.1) and Google (8.8.8.8) DNS for Android builds.

## Architecture

Kari is split into three layers. **Providers** fetch data from streaming sites (API calls or HTML scraping). **Players** send media URLs to external apps (MPV, IINA, VLC, MX Player) and track playback position via IPC. Between them sits a **Bubble Tea TUI** that handles search, episode browsing, and download management. Each provider and player is a self-contained package — adding a new one doesn't touch anything outside its own directory.

```text
cmd/kari/main.go
    |
internal/
    ├── app/           — Wires everything together
    ├── config/        — Environment config + API constants
    ├── service/       — Media, download, subtitle orchestration
    ├── provider/      — One package per streaming site
    ├── player/        — Platform-specific player backends
    ├── tui/           — Bubble Tea model-view-update
    ├── scrobble/      — Trakt.tv + AniList sync
    ├── history/       — Local JSON watch storage
    ├── downloader/    — HLS segment downloader + yt-dlp wrapper
    ├── subtitles/     — OpenSubtitles + Yify clients
    ├── aniskip/       — Fetches intro/outro timestamps
    ├── tmdb/          — Key pool with rotation
    ├── httpclient/    — Shared retryable HTTP client
    └── logging/       — Structured slog wrapper
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for more detail.

## Development

### Project structure

Same layout as above. `cmd/kari/main.go` is the entry point. Everything else lives under `internal/` — one package per concern.

### Cross-compile

```bash
chmod +x build.sh
./build.sh all     # linux/{amd64,arm64}, windows/{amd64,arm64},
                   # darwin/{amd64,arm64}, android/arm64
```

Or pick one:

```bash
./build.sh target linux amd64
```

Or build for whatever machine you're on:

```bash
./build.sh
```

### Versioning

Version is derived from git tags and baked in via `-ldflags`:

| Scenario | Example |
|---|---|
| Tagged release (`v1.0.0`) | `1.0.0` |
| 5 commits after tag | `1.0.5` (patch increments) |
| No tags yet | `1.0.42` (commit count) |
| Uncommitted changes | `1.0.0-dirty` |
| `go run` / `go build` (no ldflags) | `0.0.0-dev` |

To cut a release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

The CI builds all platforms, creates a GitHub Release, and auto-generates release notes from commits since the last tag.

### Build and verify

```bash
go build ./...
go vet ./...
go test ./...
```

### Code conventions

See [docs/CONVENTIONS.md](docs/CONVENTIONS.md) and [`AGENTS.md`](AGENTS.md) (AI assistant rules for this repo) for the full list. Key rules:

- No global state — pass dependencies explicitly
- Wire everything in `internal/app/app.go`
- Use `internal/httpclient` instead of raw `http.Client`
- Log through `internal/logging`
- Wrap errors with `%w`

## Contributing

Pull requests are welcome. The best place to start is adding a new media provider — see [PROVIDER_GUIDE.md](PROVIDER_GUIDE.md) for the full walkthrough.

A few ground rules:

- Run `go build ./... && go vet ./... && go test ./...` before opening a PR
- Don't add global state. Wire new components in `internal/app/app.go`
- Wrap errors with `%w`
- Kari does not host or distribute copyrighted content. Contributions that add DRM circumvention or direct content hosting will not be accepted.

## Troubleshooting

### macOS — "Kari cannot be opened" or "damaged"

Downloaded binaries get flagged by Gatekeeper. Clear the quarantine attribute:

```bash
xattr -d com.apple.quarantine ./kari
```

If you built from source, this doesn't apply.

### Linux — "Permission denied"

```bash
chmod +x ./kari
```

### Windows — "Windows protected your PC"

Click **More info** → **Run anyway**. SmartScreen flags unsigned binaries — the binary is clean, it just isn't code-signed.

### Android — MPV not launching

Ensure `termux-api` is installed (`pkg install termux-api`) and that you've run `termux-setup-storage`. MPV Android must be installed from the Play Store, not from Termux packages. On some devices, the `am` binary is blocked by SELinux — kari will automatically fall back to `termux-am` or `termux-am-starter` if available.

### Android — Downloads fail with DNS errors

Kari uses a built-in DNS fallback (Cloudflare/Google) on Android. If downloads still fail, check that your network connection is active. The system DNS resolver in Termux can be unreliable — this is a known Termux limitation.

### No player detected

Make sure your player is in `$PATH`. Run `which mpv` (or `which iina`, `which vlc`) to verify.

### Logs

Written to `~/.config/kari/kari.log`. Set `KARI_LOG_DEBUG=true` for verbose output.

## License

MIT
