# Kari (狩り)

![Stars](https://img.shields.io/github/stars/Dhairya3391/kari?style=flat-square&label=stars)
![License](https://img.shields.io/github/license/Dhairya3391/kari?style=flat-square)
![Go Version](https://img.shields.io/github/go-mod/go-version/Dhairya3391/kari?style=flat-square&label=go)
![Last Commit](https://img.shields.io/github/last-commit/Dhairya3391/kari?style=flat-square)

Stream anime, movies, TV shows, and cartoons directly from your terminal. External media player playback (MPV, IINA, VLC), automatic watch history, Trakt/AniList scrobbling, smart subtitle selection, and zero browser tabs.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

![kari demo](https://github.com/user-attachments/assets/be296568-f5c1-4675-86e4-626434f2829c)

---

## Quick Start

### 1. Ensure you have a media player (MPV recommended)

- **macOS:** `brew install mpv`
- **Ubuntu / Debian:** `sudo apt install mpv`
- **Arch Linux:** `sudo pacman -S mpv`
- **Windows:** `winget install mpv.net` (or standard `mpv`)

### 2. Install Kari

**macOS / Linux / Termux (one-line install):**

```bash
curl -fsSL https://raw.githubusercontent.com/Dhairya3391/kari/main/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/Dhairya3391/kari/main/install.ps1 | iex
```

*Prefer manual download?* Grab pre-compiled binaries from the [Releases page](https://github.com/Dhairya3391/kari/releases).

### 3. Launch

```bash
kari
```

Start typing what you want to watch, hit `Space` to search, and press `Enter` to play.

---

## The 30-Second Guide

1. **Search**: Type a title and hit **`Space`**. Search queries multiple providers simultaneously.
2. **Switch Category**: Press **`Tab`** to cycle between **Anime**, **Movies**, **TV Shows**, **Cartoons**, and **Jellyfin**.
3. **Pick & Play**: Choose a result with arrows or `j`/`k`, select an episode, and press **`Enter`**. Your media player launches with full playback controls.
4. **Resume**: Kari saves your progress automatically. Open watch history anytime with **`h`** and hit **`Enter`** to pick up where you left off.

You can also launch directly into a search from your command line:

```bash
kari "frieren"
```

---

## Keybindings

### Navigation & Search

| Key | Action |
| --- | --- |
| `↑` / `↓` or `j` / `k` | Move up / down |
| `Enter` | Select series, select episode, or play |
| `Esc` / `Backspace` | Go back to previous screen |
| `Space` | Execute search for current text query |
| `Tab` / `Shift+Tab` | Cycle media mode (Anime → Movies → TV → Cartoon → Jellyfin) |
| `/` | Filter list items |
| `Ctrl+H` | Return to home / search view |

### Playback & Audio

| Key | Action |
| --- | --- |
| `n` | Play next episode |
| `A` | Toggle auto-play next episode |
| `a` | Toggle Sub / Dub (Anime mode) |
| `r` | Restart episode from beginning (discards saved resume position) |
| `Ctrl+P` | Switch active playback player |

### Downloads & App

| Key | Action |
| --- | --- |
| `d` | Download selected episode |
| `x` | Stop or cancel active download |
| `h` / `H` | Open watch history |
| `s` / `S` | Open Settings (Trakt, AniList, subtitles, theme, language filters) |
| `Ctrl+D` | Toggle debug log panel |
| `q` / `Ctrl+C` | Quit Kari |

---

## Features

- **Multi-Source Parallel Search** — Queries multiple streaming sources concurrently (Miruro, MovieBox, RiveStream, VidKing, PirateX, and optional Jellyfin).
- **5 Media Categories** — Dedicated modes for Anime, Movies, TV Series, Cartoons, and private Jellyfin servers. Switch effortlessly with `Tab`.
- **Accurate Resume & History** — MPV reports timestamps back via local IPC socket. Quit anytime and Kari remembers your exact position, selected audio mode (Sub/Dub), and preferred stream language.
- **Auto-Skip Intro / Outro** — Fetches AniSkip timestamps automatically for anime and injects an on-the-fly MPV Lua script.
- **Scrobbling Sync** — Native device-flow authorization for Trakt.tv (Movies & TV) and AniList (Anime). No tokens or API keys to copy manually.
- **Smart Subtitle Fallback** — Prioritizes provider-embedded subtitles matching your preferred language, gracefully falling back to other providers and OpenSubtitles.
- **Terminal Poster Art** — Renders real pixel graphics on modern terminals (Kitty, WezTerm, Ghostty) with automatic fallback to high-density Unicode half-blocks on standard terminals.
- **Fast Accelerated Downloads** — Download episodes via `yt-dlp` with automatic `aria2c` 16-connection acceleration for fast speeds.
- **UI Customization** — Built-in theme presets and custom HEX accent color support in Settings.
- **One-Command Self Update** — Run `kari -u` to pull and install the newest release from GitHub.

---

## Media Providers

| Provider | Mode | Method | Priority |
| --- | --- | --- | --- |
| **Miruro** | Anime | API | 1 |
| **PirateX** | Cartoons | Scraper (HLS stream) | 1 |
| **VidKing** | Movies, TV | API (TMDB-indexed) | 1 (TV), 2 (Movies) |
| **MovieBox** | Movies, TV | API (TMDB-indexed) | 2 |
| **RiveStream** | Movies, TV | API (TMDB-indexed) | 2 |
| **Jellyfin** | Movies, TV | Jellyfin Server API | 1 |

*Lower priority number = primary provider. All providers are queried simultaneously in parallel.*

---

## Supported Media Players

| Player | Platforms | Features |
| --- | --- | --- |
| **[MPV](https://mpv.io/)** | Linux, macOS, Windows, Android | **Recommended.** Full support with IPC position tracking, AniSkip, and custom headers |
| **[IINA](https://iina.io/)** | macOS | MPV-based UI; position tracking supported |
| **[VLC](https://www.videolan.org/)** | Windows, Linux, macOS | Playback supported; no IPC position tracking |
| **[MX Player](https://play.google.com/store/apps/details?id=com.mxtech.videoplayer.ad)** | Android | Launched via Android intents |

---

## Optional Download Acceleration

Downloads work out of the box with `yt-dlp`. For significantly faster multi-connection downloads, install `aria2c`:

```bash
# macOS
brew install yt-dlp aria2

# Ubuntu / Debian
sudo apt install yt-dlp aria2

# Arch Linux
sudo pacman -S yt-dlp aria2

# Windows
winget install yt-dlp aria2
```

---

## Installation Methods

### Method 1: Automated Script (Recommended)

**macOS / Linux / Termux:**

```bash
curl -fsSL https://raw.githubusercontent.com/Dhairya3391/kari/main/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/Dhairya3391/kari/main/install.ps1 | iex
```

### Method 2: Manual Binary Download

Download pre-compiled binaries for your architecture from the [GitHub Releases](https://github.com/Dhairya3391/kari/releases/latest) page, extract, and place `kari` into your system `$PATH`.

### Method 3: Build from Source

Requires Go 1.26+:

```bash
git clone https://github.com/Dhairya3391/kari.git
cd kari
go build -o kari ./cmd/kari
./kari
```

---

## Configuration & Environment Variables

Kari works with zero configuration. You can optionally customize its behavior with environment variables:

### Core Settings

| Variable | Description | Default |
| --- | --- | --- |
| `KARI_PLAYER` | Force a specific media player (`mpv`, `iina`, `vlc`) | Auto-detected |
| `KARI_DOWNLOAD_DIR` | Directory where downloaded episodes are saved | `./downloads` |
| `KARI_LOG_FILE` | File destination for logs | `~/.config/kari/kari.log` |
| `KARI_LOG_DEBUG` | Enable verbose debug logging (`true` or `1`) | `false` |
| `KARI_LOG_STDERR` | Write logs to standard error (`true` or `1`) | `false` |

### Integrations & Custom API Keys

*Note: Built-in keys are provided automatically for TMDB, Trakt, and AniList. Set these only if you want to use your own personal accounts or hit rate limits.*

| Variable | Description |
| --- | --- |
| `TMDB_API_KEY` | Custom TMDB API Key. (Default: rotating key pool) |
| `JELLYFIN_URL` | Your Jellyfin server URL (e.g. `https://jellyfin.example.com`). Activates Jellyfin mode. |
| `JELLYFIN_API_KEY` | Jellyfin API Key (generate from Jellyfin Dashboard → API Keys). |
| `OPENSUBTITLES_API_KEY` | OpenSubtitles REST API key for fallback subtitles. |
| `OPENSUBTITLES_USERNAME` | OpenSubtitles username (required if API key is set). |
| `OPENSUBTITLES_PASSWORD` | OpenSubtitles password (required if API key is set). |
| `TRAKT_CLIENT_ID` / `TRAKT_ID` | Custom Trakt.tv OAuth Client ID. |
| `TRAKT_CLIENT_SECRET` / `TRAKT_SECRET` | Custom Trakt.tv OAuth Client Secret. |
| `ANILIST_CLIENT_ID` / `ANILIST_ID` | Custom AniList OAuth Client ID. |
| `ANILIST_CLIENT_SECRET` / `ANILIST_SECRET` | Custom AniList OAuth Client Secret. |

---

## Android Setup (Termux)

Kari can run on Android phones and tablets using Termux and MPV Android:

1. **Install Termux** from [F-Droid](https://f-droid.org/packages/com.termux/) *(do not use the obsolete Google Play Store release)*.
2. **Install dependencies**:

   ```bash
   pkg install curl termux-api yt-dlp aria2
   ```

3. **Grant storage permissions**:

   ```bash
   termux-setup-storage
   ```

4. **Install [MPV Android](https://play.google.com/store/apps/details?id=is.xyz.mpv)** from Google Play Store.
5. **Install Kari**:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/Dhairya3391/kari/main/install.sh | bash
   ```

6. **(One-time setup) Link MPV config bridge**:
   Open the **MPV Android app** → **Settings** → **Advanced** → **Edit mpv.conf**, and add this single line:

   ```ini
   include=/storage/emulated/0/Android/media/is.xyz.mpv/.mpv.conf
   ```

   *This allows Kari to pass necessary stream headers (Referer, Origin, Cookies) and subtitle tracks to MPV.*
7. **Run**:

   ```bash
   kari
   ```

---

## Troubleshooting

### macOS: "Kari cannot be opened" or "damaged"

macOS Gatekeeper may flag binaries downloaded via browser or curl. Remove the quarantine flag:

```bash
xattr -d com.apple.quarantine ./kari
```

### Linux: "Permission denied"

Ensure the binary has executable permissions:

```bash
chmod +x ./kari
```

### Windows: "Windows protected your PC" (SmartScreen)

Click **More info** → **Run anyway**. Pre-built binaries are clean and built via open-source GitHub Actions, but are not commercially code-signed.

### "No media player detected"

Make sure MPV, IINA, or VLC is installed and available in your system `$PATH`. Test with:

```bash
which mpv
```

### Logs & Diagnostics

Logs are written to `~/.config/kari/kari.log` (automatically rotates at 10 MB). For detailed real-time logs, start Kari with:

```bash
KARI_LOG_DEBUG=true kari
```

Or press `Ctrl+D` inside the TUI to toggle the debug panel.

---

## Development

```bash
# Run tests
go test ./...

# Vet and build
go vet ./...
go build ./...

# Multi-platform cross compilation
./build.sh all
```

See [docs/CODEBASE.md](docs/CODEBASE.md) and [`AGENTS.md`](AGENTS.md) for architectural guidelines, design patterns, and provider implementation steps.

---

## License

[MIT](LICENSE)
