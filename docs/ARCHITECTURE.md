# System Architecture

Kari is structured as a modular TUI application with clear separation between the UI, business logic (Services), and external integrations (Providers/Players).

## High-Level Diagram
```
[ CMD / Main ]
      |
[ APP / Startup ] <---- [ Config / Env ]
      |
[ TUI / Model ] <------ [ Service / Media & Download ]
      |                         |
      |                 [ Provider / Registry ]
      |                         |
[ Player / Registry ]   [ Providers / Anime, Movies, etc. ]
```

## Core Layers

### 1. Command Layer (`cmd/kari`)
The entry point. Initializes logging and calls the `app` runner.

### 2. Application Layer (`internal/app`)
Handles the "wiring" of the system. Loads the configuration, instantiates all providers, registries, and services, and passes them to the TUI.

### 3. Service Layer (`internal/service`)
The orchestration layer. 
- `MediaService`: Manages concurrent searches across providers, episode fetching, and source resolution.
- `DownloadService`: Selects the appropriate downloader for a source and manages the download lifecycle.

### 4. Provider Layer (`internal/provider`)
Implementations of different media scrapers/APIs.
- All providers must implement the `Provider` interface.
- They are registered in the `Registry` during app startup.

### 5. Player Layer (`internal/player`)
Abstractions for external media players.
- Handles platform-specific execution (e.g., MPV on Linux vs. MPV on Android via Intent).
- Manages header injection and subtitle side-loading.

### 6. TUI Layer (`internal/tui`)
Built with `charmbracelet/bubbletea`. 
- Completely decoupled from external networking or file system logic.
- Uses messages and commands to interact with Services.
