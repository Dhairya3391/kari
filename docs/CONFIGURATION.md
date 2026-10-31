# Configuration Reference

Kari runs with zero configuration by default. Optional behavior can be customized via environment variables, the interactive Settings screen (`s`), or `~/.config/kari/settings.json`.

---

## Environment Variables

### Core & Runtime

| Variable | Description | Default |
| --- | --- | --- |
| `KARI_PLAYER` | Force a specific media player binary (`mpv`, `iina`, `vlc`). | Auto-detected from `$PATH` |
| `KARI_DOWNLOAD_DIR` | Directory where downloaded episodes and media files are stored. | `./downloads` |
| `KARI_IMG_PROTOCOL` | Override terminal graphics protocol (`kitty`, `iterm`, `sixel`, `blocks`, `none`). | Auto-detected |
| `KARI_DISCORD_APP_ID` | Override Discord Rich Presence application client ID. | Bundled default |
| `KARI_MPV_ANDROID_PACKAGE` | Android mpv client package name used for availability checks, launch intents, and the config bridge directory. | `is.xyz.mpv` |
| `MPV_SOCKET` | Override MPV Unix IPC socket path or Windows named pipe. | Auto-generated per process |

### Logging & Diagnostics

| Variable | Description | Default |
| --- | --- | --- |
| `KARI_LOG_FILE` | Destination file for application logs (automatically rotates at 10 MB). | `~/.config/kari/kari.log` |
| `KARI_LOG_DEBUG` | Enable verbose debug logging (`true` or `1`). | `false` |
| `KARI_LOG_STDERR` | Mirror log output to standard error (`true` or `1`). Suppressed if stderr shares terminal with TUI. | `false` |
| `KARI_LOG_FORMAT_JSON` | Output logs in structured JSON format (`true` or `1`). | `false` (plain text) |

### Integrations & Custom API Keys

*Bundled public keys and client IDs are included out of the box for TMDB, Trakt, AniList, Anime-Skip, and Pengu. The variables below cover the integrations that still accept overrides.*

| Variable | Description |
| --- | --- |
| `JELLYFIN_URL` | Jellyfin server URL (e.g. `https://jellyfin.example.com`). Activates Jellyfin mode. |
| `JELLYFIN_API_KEY` | Jellyfin API Key (generated from Jellyfin Dashboard → API Keys). |
| `TMDB_API_KEY` | Custom TMDB v3 API Key. Falls back to built-in rotating key pool. |
| `OPENSUBTITLES_API_KEY` | OpenSubtitles REST API key for fallback subtitles. |
| `OPENSUBTITLES_USERNAME` | OpenSubtitles account username (required when API key is set). |
| `OPENSUBTITLES_PASSWORD` | OpenSubtitles account password (required when API key is set). |

---

## Settings File (`settings.json`)

User preferences configured via the in-app Settings screen (`s`) are persisted to `~/.config/kari/settings.json`.

```json
{
  "quality_mode": 0,
  "download_quality": 0,
  "disable_download_verification": false,
  "language_filter": {
    "en": true,
    "ja": true
  },
  "subtitle_language": "en",
  "disable_anime_subtitles": false,
  "default_anime_audio": "sub",
  "anime_title_language": "romaji",
  "preferred_player": "mpv",
  "autoplay": false,
  "disable_images": false,
  "accent_color": "auto",
  "skip_provider": "hybrid",
  "auto_skip_intro": false,
  "auto_skip_ending": false,
  "skip_recap": false,
  "skip_preview": false,
  "startup_sync": false,
  "discord_presence": false,
  "default_mode": "last",
  "modes": [
    "anime",
    "cartoon",
    "live",
    "manga",
    "movies",
    "tv",
    "jellyfin"
  ],
  "modes_disabled": [],
  "disabled_providers": [],
  "last_mode": "anime",
  "transitions": true,
  "startup_search_active": true,
  "auto_update": false
}
```

### Fields

| Field | Type | Description | Default |
| --- | --- | --- | --- |
| `quality_mode` | int | Stream playback quality: `0` (Best/Highest), `1` (1080p), `2` (720p), `3` (Lowest/Data Saver). | `0` |
| `download_quality` | int | Target quality for episode downloads. | `0` |
| `disable_download_verification` | bool | Skip post-download container and audio stream integrity checks. | `false` |
| `language_filter` | map | Allowed audio language codes for movies, TV, and cartoons (e.g. `{"en": true}`). | `{}` (all) |
| `subtitle_language` | string | Preferred ISO 639-1 subtitle language code (e.g. `"en"`, `"es"`, `"fr"`). | `"en"` |
| `disable_anime_subtitles` | bool | Suppress subtitles specifically for anime (useful when watching English dubs). | `false` |
| `default_anime_audio` | string | Startup audio mode for anime: `"sub"` or `"dub"`. | `"sub"` |
| `anime_title_language` | string | Title display language: `"romaji"` or `"english"`. | `"romaji"` |
| `preferred_player` | string | Target media player binary name (`"mpv"`, `"iina"`, `"vlc"`). | `""` (auto) |
| `autoplay` | bool | Automatically start the next episode upon current episode completion. | `false` |
| `disable_images` | bool | Disable in-terminal poster art and manga image rendering. | `false` |
| `accent_color` | string | UI accent color: `"auto"` (adapts to active mode), hex (`"#7aa2f7"`), or preset. | `"auto"` |
| `skip_provider` | string | Skip resolution engine: `"hybrid"`, `"skipdb"`, `"introdb"`, `"anime-skip"`, `"aniskip"`, `"off"`. | `"hybrid"` |
| `auto_skip_intro` | bool | Automatically jump past opening sequences without waiting for confirmation. | `false` |
| `auto_skip_ending` | bool | Automatically jump past outro sequences. | `false` |
| `skip_recap` | bool | Automatically skip detected recap segments. | `false` |
| `skip_preview` | bool | Automatically skip preview sequences at the end of episodes. | `false` |
| `startup_sync` | bool | Sync watch history from AniList and Trakt on application launch. | `false` |
| `discord_presence` | bool | Broadcast current title, episode, and playback state to Discord Rich Presence. | `false` |
| `default_mode` | string | Mode to open on startup: `"last"`, `"first"`, or a specific key (`"anime"`, `"movies"`). | `"last"` |
| `modes` | list | Ordered list of content modes displayed in tab bar. | Canonical list |
| `modes_disabled` | list | List of disabled mode keys excluded from tab switching. | `[]` |
| `disabled_providers` | list | List of disabled provider IDs (e.g. `["pengu", "cinecat"]`). | `[]` |
| `transitions` | bool | Enable color crossfade transitions between views. | `true` |
| `startup_search_active` | bool | Focus the search input field immediately on application launch. | `true` |
| `auto_update` | bool | Check and prompt for background updates when a new release is published. | `false` |

---

## File Locations

| Path | Purpose |
| --- | --- |
| `~/.config/kari/settings.json` | Persistent user configuration and preferences. |
| `~/.config/kari/history.json` | Local watch history database with timestamps and audio settings. |
| `~/.config/kari/kari.log` | Rotating application runtime log file (10 MB cap). |
| `~/.config/kari/tokens/` | OAuth credentials for AniList (`anilist.json`) and Trakt (`trakt.json`). |
| `~/.config/kari/cache/posters/` | Downloaded cover and poster artwork cache. |
| `~/.config/kari/cache/manga/pages/` | Prefetched manga chapter pages for in-terminal reader. |
| `~/.config/kari/cache/subtitles/` | Downloaded OpenSubtitles sidecar files. |
| `/usr/local/share/man/man1/kari.1` | Installed Unix man page (`man kari`). |
