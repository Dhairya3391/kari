# Keybindings Reference

All keybindings are active inside the Kari TUI. Context-sensitive keys are available on their respective screens.

---

## Global Keys

| Key | Action |
| --- | --- |
| `q`, `Ctrl+C` | Quit Kari |
| `Ctrl+H` | Return to Home / Search view from any screen |
| `Ctrl+P` | Cycle active media player (`mpv` → `iina` → `vlc`) |
| `Ctrl+D` | Toggle the real-time debug log overlay |
| `?` | Toggle keybinding cheat sheet overlay |

---

## Navigation & Search (Home / Results)

| Key | Action |
| --- | --- |
| `Space` | Focus search input (or execute search query) |
| `Enter` | Select title / open series / play stream |
| `Esc` | Blur search box (switches to control mode) / go back |
| `Tab` | Switch active content category forward (`Anime` → `Cartoon` → `Live` → `Manga` → `Movies` → `TV` → `Jellyfin`) |
| `Shift+Tab` | Switch active content category backward |
| `1` – `7` | Jump directly to category (in control mode, when search input is blurred) |
| `j` / `k` or `↓` / `↑` | Move cursor down / up |
| `g` / `G` | Jump to top / bottom of list |
| `/` | Enter list filter mode to narrow results |
| `h` | Open Watch History |
| `s` | Open Settings |
| `d` | Open Downloads screen |

---

## Series & Episode View

| Key | Action |
| --- | --- |
| `[` | Switch to previous season (multi-season TV & Anime) |
| `]` | Switch to next season |
| `Enter` | Select episode / resolve streams |
| `a` | Toggle Sub / Dub audio track (Anime mode) |
| `m` | Toggle episode marked as watched / unwatched |
| `t` | Show anime airing status and episode count overlay |
| `Space` | Toggle multi-select checkbox for batch download |
| `Ctrl+A` | Select all episodes for batch download |
| `Ctrl+D` | Deselect all episodes |
| `D` | Start batch download of selected episodes |
| `Esc` | Return to search results |

---

## Stream Preview Page

| Key | Action |
| --- | --- |
| `Enter`, `p` | Launch media player with currently selected stream source |
| `j` / `k` or `↓` / `↑` | Move cursor between available stream resolutions and mirrors |
| `r` | Restart episode from 00:00 (discards saved resume timestamp) |
| `R` | Retry failed provider stream resolution |
| `a` | Toggle Sub / Dub audio mode |
| `A` | Toggle auto-play next episode on/off |
| `n` | Play next episode |
| `m` | Mark current episode as watched |
| `d` | Go to downloads screen |
| `D` | Queue selected stream for download |
| `Esc` | Return to episode list |

---

## In-Terminal Manga Reader

| Key | Action |
| --- | --- |
| `l`, `→`, `PgDn` | Next page |
| `h`, `←`, `PgUp` | Previous page |
| `n` | Next chapter |
| `p` | Previous chapter |
| `Esc` | Exit reader and return to chapter list |

---

## Watch History (`h`)

| Key | Action |
| --- | --- |
| `Tab` | Switch between **Continue** and **Finished** buckets |
| `Enter` | Resume selected title from saved timestamp |
| `m` | Toggle marked as watched |
| `d` | Remove selected title from history |
| `D` | Clear all watch history (prompts confirmation) |
| `Esc` | Return to previous screen |

---

## Settings Screen (`s`)

| Key | Action |
| --- | --- |
| `Tab` | Cycle through Settings category tabs |
| `Shift+Tab` | Cycle Settings category tabs backward |
| `1` – `7` | Jump directly to category tab (1: Playback, 2: Languages, 3: Anime, 4: Modes, 5: Providers, 6: Accounts, 7: Interface) |
| `j` / `k` or `↓` / `↑` | Move cursor between settings |
| `h` / `l` or `←` / `→` | Change selected value / toggle provider |
| `Enter`, `Space` | Select / edit / activate toggle |
| `J` / `K` (or `Shift+↓` / `Shift+↑`) | Reorder content modes (under Modes tab) |
| `Esc` | Save and exit Settings |

---

## Downloads Screen (`d`)

| Key | Action |
| --- | --- |
| `p` | Pause / resume active download |
| `Enter` | Play completed download (or open folder if on active download) |
| `o` | Open download directory in system file manager |
| `x` | Cancel active download / Delete completed file from disk (2×) |
| `X` | Cancel all downloads (2×) |
| `j` / `k` or `↓` / `↑` | Move cursor between download items |
| `g` / `G`, `Home` / `End` | Jump to first / last item |
| `PgUp` / `PgDn`, `Ctrl+U` / `Ctrl+D` | Page up / down, half page scroll |
| `Esc` | Return to previous screen |
