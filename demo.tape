# VHS tape for Kari (https://github.com/charmbracelet/vhs)
# Generates the official Kari demo GIF showcasing:
# - Launch and switching to TV Shows mode (Escape + Tab x 4)
# - Searching Breaking Bad across parallel providers
# - TV series multi-season navigation via '[' and ']'
# - Full Stream Preview Page with resolutions, providers, audio, subtitles, and skip times
# - Tabbed navigation through all 7 Settings categories
# - Watch History and Keybinding Cheat Sheet overlays
#
# Generate demo GIF:
#   vhs demo.tape

Output assets/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 750
Set Padding 20
Set Theme "Catppuccin Mocha"
Set TypingSpeed 75ms

# Ensure local build is in PATH and terminal starts clean
Type "export PATH=$PWD:$PATH"
Enter
Sleep 300ms
Type "clear"
Enter
Sleep 500ms

# 1. Launch Kari interactively
Type "kari"
Enter
Sleep 2.5s

# 2. Blur search input with Escape, then press Tab 4 times to switch to TV mode
Escape 1
Sleep 800ms
Tab 1
Sleep 600ms
Tab 1
Sleep 600ms
Tab 1
Sleep 600ms
Tab 1
Sleep 1.5s

# 3. Press Space to focus search input in TV mode, type "breaking bad" and search
Space 1
Sleep 600ms
Type "breaking bad"
Sleep 800ms
Enter 1
Sleep 4.5s

# 4. Select Breaking Bad (row 0) to open episodes view
Enter 1
Sleep 3.5s

# 5. Showcase multi-season handling with '[' and ']'
# Season 1 is active (Pilot, Cat's in the Bag...)
Sleep 2s

# Switch forward to Season 2 (Seven Thirty-Seven, Grilled...)
Type "]"
Sleep 2.2s

# Switch forward to Season 3 (No Más, Caballo sin Nombre...)
Type "]"
Sleep 2.2s

# Switch forward to Season 4 (Box Cutter, Thirty-Eight Snub...)
Type "]"
Sleep 2.2s

# Switch backward through seasons with '['
Type "["
Sleep 1.6s
Type "["
Sleep 1.6s
Type "["
Sleep 2s

# Inspect Season 1 episodes and synopsis pane
Down 1
Sleep 1.2s
Down 1
Sleep 1.2s
Up 1
Sleep 1s
Up 1
Sleep 1.5s

# 6. Press Enter on Episode 1 (Pilot) to showcase the Stream Preview Page
# Resolves streams across providers in parallel, ranking resolutions & health
Enter 1
Sleep 5.5s

# Browse ranked stream sources on the Preview screen
Down 1
Sleep 1.2s
Down 1
Sleep 1.2s
Up 1
Sleep 1.2s

# 7. Press Escape to return to Episodes view, then press 's' to open Settings
Escape 1
Sleep 1.2s
Type "s"
Sleep 2.5s

# Tab through Settings categories:
# 1. Playback -> 2. Languages
Tab 1
Sleep 2s
# 2. Languages -> 3. Anime & Skip Engines
Tab 1
Sleep 2s
# 3. Anime -> 4. Modes Management
Tab 1
Sleep 2s
# 4. Modes -> 5. Providers Toggle List
Tab 1
Sleep 2s
# 5. Providers -> 6. Accounts (Trakt, AniList, Discord Presence)
Tab 1
Sleep 2s
# 6. Accounts -> 7. Interface (Themes & Accent Colors)
Tab 1
Sleep 2s
# Back to 1. Playback
Tab 1
Sleep 1.8s

# Close Settings back to Episodes
Escape 1
Sleep 1.5s

# 8. Open Watch History with multi-category progress tracking
Type "h"
Sleep 3s
Escape 1
Sleep 1.2s

# 9. Open in-TUI keybinding cheat sheet overlay
Type "?"
Sleep 3s
Escape 1
Sleep 1.2s

# 10. Clean exit
Type "q"
Sleep 1s
