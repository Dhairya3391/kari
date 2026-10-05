# Providers — facts (Phase A)

> Generated from code reading + `go vet`/`go test` baseline (2026-09-21).
> KARI_AUDIT.md and docs/KARI_REFACTOR_PLAN.md are absent from the repo, so
> this file documents current behavior as measured. The new
> `media.Kind / Catalog / Sourcer / Streamer / Reader` interfaces in
> `internal/provider/provider.go` (MediaSource/routes) and `internal/model/kind.go` (Kind/KindSpec)
> exist but no provider implements them yet; all live paths use the legacy
> `provider.Provider` contract (`internal/provider/provider.go:209-215`).
> No domain unification is done here per task rules.
> Secrets redacted: domains only, never full stream URLs.

## 0. Registration (single source today)

- `internal/provider/defaults/defaults.go` is the only registration
  list: `anicine, movysx, anikoto, anilight, miruro, moovie, weebcentral, pengu, jellyfin`.
- `jellyfin.When` gates on `JellyfinURL && JellyfinAPIKey`
  (`defaults.go:60-62`); factory failure skips with log, never fatal
  (`defaults.go:80-84`).
- `Registry.ProvidersForMode` filters by `Modes()` + stable sort by
  `Priority` lower-first (`registry.go:24-54`).

Declared modes/priorities:

| mode | providers (priority) | evidence |
| --- | --- | --- |
| anime | anicine P1, anikoto P2, miruro P2, anilight P3 | `anicine`, `anikoto`, `miruro`, `anilight` `client.go` `Modes()` |
| movies/tv/cartoon | anicine P1, movysx P2, pengu P2, moovie P2 | `anicine`, `movysx`, `pengu`, `moovie` `client.go` `Modes()` |
| live | pengu P1 only | `pengu/client.go:162` |
| manga | weebcentral P1 only | `weebcentral/client.go:48-50` |
| jellyfin | jellyfin P1 only | `jellyfin/client.go:55-59` |

Cartoon search reuses TMDB multi-search + genre-16 filter via streambase
(`streambase/base.go:52-66,114-169`).

## 1. Current routing

### Search: parallel-merge with fast-return grace (all modes)

- TUI entry `tui/update_selection.go:178-202` → `MediaService.Search`;
  history resume reuses it (`update_history.go:208,214,223`).
- `service/media_service.go:38-45`: `ProvidersForMode(mode)`, overall
  `12s` timeout. One goroutine per provider (`54-60`), buffered chan.
- Collect loop (`66-84`): on first `err==nil && len>0` sets 1.5s grace
  timer; exits on grace expiry or `ctx.Done()`. Not first-wins; waits for
  stragglers up to 1.5s after first success, else full 12s.
- Merge/order/dedupe (`91-112`): iterate providers in priority order, stamp
  `r.Provider`; dedupe key `Type:TrimSpace(ID)`, empty ID never deduped.
- Fallback triggers (`114-133`): `len==0` → `ctx.Err()` if deadline;
  all `ErrNoResults` → `ErrNoResults`; else first warning as error.
  Missing entries (no send before deadline) become `"TIMED OUT"` warnings
  (`93-99`) but break `allNoResults` (treated as failure, not empty).
- Manga search stays on generic `MediaService` (`manga_service.go:16-18`).
- Bug facts: no per-provider timeout (shared 12s ctx); a single hung
  provider holds the collect loop until overall deadline when nobody
  succeeds; slow provider delays results up to 1.5s grace even when first
  results are usable; timeout warnings are strings, not typed, and the UI
  cannot distinguish timeout vs error without parsing.

### Items (episodes): single-origin, no fan-out

- `service/media_service.go:141-164`: overall `45s`,
  `ProviderByNameForMode(series.Provider)`; miss → user-facing
  "unavailable in %s mode" error. Audio filter `matchesAudioMode`
  (`169-184`): empty matches all, prefix-normalized sub/dub.
- Movies/live bypass: `tui/update_selection.go:42` synthesizes
  `Episode{ID: series.ID}` when `MediaType==movie|live` and
  `!RequiresEpisodeListForMovies(provider)`. Default false
  (`registry.go:154-167`); true only for anikoto/anilight/reanime
  (`anikoto/client.go:42-46`, `anilight/client.go:42-45`,
  `reanime/client.go:42-43`). So movysx/pengu/jellyfin movies resolve
  directly.

### Sources: parallel-merge all mode providers + progressive snapshots

- TUI `update_selection.go:221-255`: outer `60s` ctx, progressive
  `onResult → resolveChan`; calls `MediaService.Resolve`.
- Service `media_service.go:188-196`: `ProvidersForMode(mode)`, overall
  `45s`. `errgroup` fan-out (`236-331`).
- Cross-provider ID (`241-263`): other-provider uses `series.TMDBID` if
  `>0`, else anime reuses `series.ID`, else skips provider silently.
  `tmdbID = series.TMDBID || episode.TMDBID || Atoi(series.ID)`.
- No provider implements `StreamingProvider` yet
  (`provider.go:284-289`); the branch (`284-305`) is dead until a provider
  does. All go through `ResolveSource` wrapped as 1-item chan.
- Aggregator (`350-447`): stamps `Resolver`; dedupes playback by
  `TrimSpace+TrimSuffix("/")` URL (query significant, `427-447`); subs
  dedupe by `sub.URL`, label `LangName(DisplayName)` (`383-396`).
- Sort (`403-425`, provider-specific branching — see §6): anikoto
  Vidstream-2 top, then movysx top, then `SourceQuality` desc, then
  registry priority. TUI mirrors it (`playback_helpers.go:63-73`,
  `update_results.go:572,588`).
- Failure (`333-347`): zero sources → `ctx.Err()` or joined failure
  strings or `ErrNoSources`. Failures are strings; ranking later reads
  `FailedProviders map[string]time.Time` but resolve does not feed it.

### Manga: single-owner + sequential cross-source fallback

- `service/manga_service.go:28-40` capability assert, no name switch.
- `FetchChapters 30s` (`43-59`), `FetchPages 30s` (`170-183`).
- `ResolveReadableChapter 60s` overall (`78-152`): own chapters →
  `findChapterNumber` match; loop `ProvidersForMode(ModeManga)` priority
  order with per-call `15s` search / `20s` chapters / `20s` pages;
  cap 3 titles; first `len(pages)>0` wins; else `ErrNoSources`.
- TUI `update_manga.go`: `chaptersCmd 65-73`, `pagesCmd 174-182`,
  `fallbackCmd 245-253`; `ErrNoSources` → auto-advance 3 then one
  cross-source try (`198-218`); page fetch `45s` (`373-375`).

### Timeouts (overall vs per-provider)

- Service: search `12s`+1.5s grace, episodes `45s`, resolve `45s`
  (`media_service.go:44,142,195`).
- Manga: `60s` overall + `15s/20s/20s` per-call (`manga_service.go:78-152`);
  direct chapters/pages `30s` each.
- TUI wrappers: resolve `60s` (`update_selection.go:227`), subtitles
  `30s`, reader page `45s` (`update_manga.go:373`).
- No per-provider search/source deadline; per-provider HTTP inherits the
  shared ctx and relies on `httpclient` 15s timeout (`httpclient.go:18`).

## 2. Identity mapping

- Search→episodes: same `SearchResult.ID` back into originating provider's
  `FetchEpisodes` (`media_service.go:152`). No translation.
- Search→sources cross-provider (`media_service.go:241-263`): TMDB ID when
  present; anime falls back to raw `series.ID` (AniList numeric ID reused
  across anikoto/anilight/reanime, which are all AniList-keyed); others
  skip. No title-based resolution; no confidence score; no cache.
- Absolute vs seasonal: anime episode `ID` encodes
  `watch/<server>/<anilistID>/<sub|dub>/<number>`; `kit.ParseHandle`
  (`kit/kit.go:46-74`) parses 5/4/3-part forms. Number is the provider's
  episode number, passed through as `Episode.Episode`. No absolute↔seasonal
  conversion at edges; One Piece-style absolute numbering works only
  because all three anime providers share the AniList numbering.
- External IDs for skip/scrabble/subtitles/posters — per-package
  re-derivation, no shared `ExternalIDs`:
  - Skip: `aniskip` needs MAL ID (`aniskip/resolver.go:GetMALID`), `animeskip`
    needs AniList ID (`animeskip/client.go:GetTimestamps`); callers derive
    from title strings per call site.
  - Scrobble: `service/scrobble_service.go` + `scrobble/trakt.go`,
    `scrobble/anilist.go` derive from `series.TMDBID`/title per call.
  - Subtitles: `service/subtitle_service.go:37-257` tries provider subs
    then OpenSubtitles by title text.
  - Posters: `poster/` TMDB by `TMDBID`, AniList by title; `SearchResult`
    carries `TMDBID int` (`provider.go:75`) and `CoverURL/CoverReferer`
    stamped by provider only when catalog ships art (weebcentral)
    (`provider.go:76-85`).
- Gap: no lazy cached `ExternalIDs{anilist,mal,tmdb,imdb}` on Title; every
  consumer re-derives; no `Mapper.Resolve` with threshold.

## 3. Field mapping

Unified shapes: `provider.SearchResult` (`provider.go:64-97`),
`provider.Episode` (`101-111`), `provider.MediaSource` (`116-138`),
`MangaChapter/Page` (`143-167`); 

| provider | raw → unified | gaps / inconsistencies |
| --- | --- | --- |
| anikoto | `/search?q=` JSON `{id(json.Number),name,format,year}` → `Title/ID/MediaType(movie iff format==MOVIE)/Year`; episodes JSON `{number float64}` → `Episode.Episode`; watch `/watch/<srv>/<id>/<cat>/<n>` via `kit.FetchFirst` servers `l,light,mello,vid`-like list → `MediaSource{URL,Quality:"Auto (…)",Referer: megaplay.buzz, Type hls/mp4 by ext}` | quality strings are server labels (`"Auto (HD1)"`), not heights; audio from handle category sub/dub only; subtitle langs via `kit.Subtitles` + `lang.Normalize`; size absent; headers per-source Referer, no Cookie |
| anilight | same shape, base `noob3`, referer `anilight.live`, servers `[l,light,mello,vid]` (`anilight/client.go:237`); `cleanAniLightText`, `pickTitle` | same gaps as anikoto; title cleaning ad-hoc per provider |
| reanime | same shape, base `noob4`, referer `flixcloud.cc`, servers `[hd1,hd2]` (`reanime/client.go:230`); `ArgPolicy{KeepHeaderFieldArgs,SkipURLArgs,StripQuotes}` (`reanime/client.go:254`) | same gaps; MPV arg filtering differs per provider |
| movysx | search/items delegate to `streambase.Base` (TMDB); resolve `?tmdb=&type=movie\|tv&s=&e=` JSON `{sources[],audio[],subtitles[]}` → `MediaSource{URL,Quality raw,Referer movy.sx,Type declared-first (hls/mp4/dash/auto)}`; `audio[]` dubbed rows append as `Language`-tagged sources (`Auto (Hindi)`); 504 → `ErrTimeout` | quality raw upstream; main rows untagged, dubbed rows ISO (`hi`, `und` kept); subtitles ISO via `lang.Normalize`; no size; key-pool rotation in streambase (`isAuthError 401/429`) |
| pengu | TMDB base for search/items + own `/api/...` resolve; live catalog fan-out (`client.go:399-411`); `Quality "4K [4KHDHub] (Hindi)"` (`client_test.go:126`); `mapAudioLanguage("Hindi, English")→"hi"` (`client_test.go:231-233`); `Type hls/mp4` by ext (`client.go:700`); `Referer` per-stream host, `UserAgent DesktopUA`, `CookieHeader` passthrough | language normalized to ISO in one helper but quality embeds `[TAG]` + `(Lang)`; live `StartsAt` parsed by `parseLiveStart` (local-time conversion must be verified live); deflated `z…` config token (`buildConfigSegment`); 429 → `ErrRateLimited` |
| weebcentral | HTMX scrape `weebcentral.com`; search ≤25; chapters from series page + `chapter-select` merge; pages `pageImgSrcRe`; `MangaPage{URL,Referer: site, UserAgent: DesktopUA}` | `FetchEpisodes/ResolveSource` return sentinels (correct for Reader); chapter numbers strings (`"10.5"`); year/genres scraped (`yearRe`, `seriesTitleRe`); throttle 300ms mutex (`client.go:89-103`) |
| jellyfin | `X-Emby-Token` `authGET`; library cached 5m (`library.go:20`), `maxSearchResults 50`; `rankLibrary/matchScore/hasWordPrefix/containsAllWords/isSubsequence`; resolve synthetic `/Videos/{id}/stream?static=true&api_key=` `Type mp4 Quality "Jellyfin"` (`client.go:237-239`) | quality fixed string; no audio/subtitle langs from server yet; search falls back to `searchHints` when cache misses |
| streambase | TMDB multi-search + details + `errgroup` seasons sem 5 (`base.go:35-472`); key rotation `NextKey/MarkFailed` on 401/429 | movie → synthetic single episode; cartoon filter genre 16; `reEpisodeNum/reSeasonNum` package regexps (`base.go:28-29`) |

Normalization gaps (repo-wide): quality strings mix heights (`1080p`),
labels (`FHD`, `Auto`, `4K [TAG] (Hindi)`), dims; languages mix ISO
(`hi`), names (`Hindi`), display (`Hindi, English`); `SourceTypeHLS/M3U8/MP4`
emitted by ext check per provider, not one helper; dub/sub from
handle/episode audio tag, not canonicalized; dedupe by exact URL (+trailing
slash trim) in `media_service.go:427-447`; no canonical `Name/Alias`
beyond `Registry.DisplayName`; headers derived per provider, not via a
single `Headers()` (legacy `MediaSource` has fields; new `media.Source`
has `Headers()` in `media/media.go:65-82` but providers don't emit it).
Live: `SearchResult{Live,StartsAt,Group}` (`provider.go:92-96`) + media
`Title{Live,StartsAt,Group}` (`media.go:22-29`); no schedule text in Year
by contract, but must verify live parsing.

## 4. Error taxonomy actually used

Sentinels `internal/provider/errors.go:9-16`: `ErrNoResults, ErrNoEpisodes,
ErrNoSources, ErrNotFound, ErrAuthRequired, ErrRateLimited`.
`HTTPError{Code,URL}` (`19-42`) implements `StatusCodedError`
(`provider.go:293-295`).

- Providers: `fmt.Errorf("<pkg> <op>: …: %w")` + sentinels + `&HTTPError{}`
  on non-200. `kit.FetchFirst` maps 404→`ErrNotFound`, else `HTTPError`,
  empty→`ErrNoSources` (`kit/kit.go:99-140`). Pengu maps 429/`rate_limited`
  →`ErrRateLimited`, `signin.mp4`→`ErrAuthRequired`.
- Service: `Search` empty→`ErrNoResults` or first warning string
  (`media_service.go:114-133`); `Resolve` zero sources→`ctx.Err()` or joined
  failure strings or `ErrNoSources` (`333-347`).
- Missing kinds: no `Blocked(403/challenge)`, `Timeout`, or
  `UpstreamChanged` (structure-missing) types; parse failures
  (`json.Decode`, HTML regex misses) surface as wrapped decode errors or —
  worse — empty slices that callers turn into `ErrNoResults/ErrNoSources`,
  masquerading as "no results". No redacted-hint convention.
- Expected fan-out failures log at Debug (`CODEBASE.md §7`); resolve
  all-fail logs Warn with joined strings.

## 5. HTTP client facts

- One shared client: `internal/httpclient/httpclient.go:22-69`.
  `New()` everywhere (`anikoto:65`, `anilight:64`, `reanime:62`,
  `movysx:68`, `weebcentral:81`, `jellyfin:40`, `streambase:45`);
  `NewWithUserAgent` only for pengu (`pengu/client.go:144`).
  No `&http.Client{}` in provider tree; sole raw client is aria2 local RPC
  (`downloader/aria2_rpc.go:31`, sanctioned non-idempotent).
- Transport: `MaxIdleConns 100`, `MaxIdleConnsPerHost 16`
  (`httpclient.go:56-57`); `IdleConnTimeout 90s`; HTTP/2 + gzip default
  (no code sets `Accept-Encoding` manually — verified by grep); keep-alive
  on; resilient DNS dialer (system first, public fallback, TCP-preferring;
  Android always public) (`112-163`); dial `30s`/keepalive `30s`.
- Retries: `retryablehttp`, `RetryMax 2`, `200–1500ms` backoff
  (`44-46`); retries idempotent GETs via standard client; local aria2
  excluded. Worst case per call ≈ `timeout 15s` (whole-client timeout
  bounds retries) — but service ctx (12s/45s) usually fires first.
  429 `Retry-After` honored by retryablehttp defaults.
- Per-host concurrency: no semaphore today (task cap 4 missing); transport
  allows 16 idle/host. UA: per-request default only for pengu; others rely
  on Go default or per-request headers; `KariClientHeader` injected on all
  (`79-83`) for broggl-farm challenge bypass.
- Body limits: none — `json.Decode`/`io.ReadAll` unbounded in
  `kit.FetchFirst`, provider clients, streambase. No `io.LimitReader` cap
  (task wants 8 MB).
- Regexp compilation: package-level `var` only — pengu 3
  (`pengu/client.go:47-49`), weebcentral 7 (`client.go:137,219,235,274,
  318,321,421`), streambase 2 (`base.go:28-29`), ranking 11
  (`ranking.go:45-57`). Anime providers/kit/jellyfin compile none per call.
- Bubble Tea Update: no direct provider call found in Update paths —
  `update_selection.go:178-255` and `update_manga.go:65-253` issue `tea.Cmd`
  closures that call services off the UI thread; `resolveChan/downloadChan`
  bridge (`CODEBASE.md §10`). `FetchFirst` sequential `Do+ReadAll` loop
  runs inside those cmds, not in Update itself. Verify with grep for
  `.Search(/.ResolveSource(/FetchEpisodes(` under `internal/tui` — only
  inside `*Cmd` constructors.

## 6. Provider-specific branching (must go)

- `service/media_service.go:406-416`: sort prefers anikoto Vidstream-2,
  then movysx — name switches in generic service.
- `tui/playback_helpers.go:63-73` `movyFirst` re-partitions movysx on top
  (comment admits mirroring service); `update_results.go:572,588` comments.
- Capability/mode branches that stay (not name branches):
  `update_selection.go:34,42`, `content_mode.go:59-64`,
  `view_layout.go:810` jellyfin note, `update_settings.go:608-624`.
- `staticcheck` baseline: 1 hit —
  `tui/view_layout.go:816:14 SA4010 append never used` (pre-existing).

## 7. Operating rules (enforced in code)

- Routing comes only from `internal/provider/routes.go` (Kind →
  []Route, Merge/Fallback/Race/Single; search ≤8s, sources ≤12s,
  plus the overall deadline). Registry, MediaService and the TUI read
  the table; no provider-name switches in service or tui. Sort
  preference is data too (`BoostRank`/`PreferredResolvers`): anime
  prefers Anikoto Vidstream-2, movies/tv/cartoon prefer Movy.sx —
  movy-first is a routing-table fact, not a code branch.
- Idempotent loads: one media pipeline at a time. `guardLoad`
  (`internal/tui/load_guard.go`) refuses a new user-initiated load
  while anything runs, naming what runs ("Please wait — resolving
  streams"). Completion chains (autoplay, auto-advance, history steps)
  arrive settled and never hit the guard.
- Play readiness: preview enter starts playback once the first source
  AND the first subtitle are in (or subtitles are off/settled). Until
  then enter reports "Resolving playback streams…" /
  "Loading subtitles…".
- Partial results ship with the failed-provider list
  (`MediaService.LastFailures`, fed by the health breaker: 3 failures
  in 5 min → 2 min break, lone routes never skipped). Preview shows
  "N of M providers · K found" live and "✗ X timed out · r retry".
- Display names the backend, not the aggregator: the `[Tag]` in the
  quality label (`sourceBackendName`), falling back to the registry
  alias. Live mode never opens the now-playing screen (no
  position/duration exists); playback stays on preview with
  "● playing — x to stop".
- Posters: w500 source, memory + disk caches, prefetch at resolve
  start; the column never collapses — "loading image…" /
  "no image found" placeholders (Kitty slot cleanup preserved).
- HTTP: shared client, per-host cap 4, GET-only retries with
  Retry-After, 8 MB JSON/HTML body cap (`ReadCapped`), stable desktop
  UA, no manual Accept-Encoding. No ResponseHeaderTimeout: broggl-farm
  header waits routinely exceed 10s; the client (15s) and service
  deadlines bound calls instead.
- Error taxonomy: `*ProviderError{Provider, Stage, Kind, Hint}`
  (`errors.go`); `errors.Is` against the old sentinels still works.
  Parse drift is `UpstreamChanged` with a redacted hint — never
  "no results".
- Field normalization lives in `provider/kit/normalize.go`; identity
  in-memory, with
  `ExternalIDs` on the title and canonical `EpisodeKey`.

## 8. How to fix a provider / add a provider (current)

- Fix: point `NewClientWithBaseURL` at `httptest` fake or re-record live;
  run `go test ./internal/provider/<name>/…`; live path
  `go test -tags live ./tests/live -run 'TestProviderPipeline/<mode>/<name>'`.
  Offline harness: `internal/provider/providertest` (redacting replay,
  scripted Fake, `Run` contract suite, Chaos transport). Reference
  rollout: `internal/provider/anikoto/contract_fixture_test.go`
  (fixtures), `internal/provider/jellyfin/contract_fixture_test.go`
  (fake API). Playability: 1 KB probes (HLS chain or MP4 range;
  TS 0x47 / ftyp / EBML / EXT-X-KEY accepted), domains only in reports.
- Add: constants in `config/constants.go`, package under
  `internal/provider/<name>/` with private API types + `Provider` (+
  optional `AudioLanguagesSource/MovieEpisodeFlow/FeatureSource/
  StreamingProvider/Presenter` + guards), one `Descriptor` in
  `defaults.go:21-67`, one `Route` row in `routes.go`, fixtures +
  `providertest.Run`. Registry/TUI pick it up; verify with
  `go vet ./... && go build ./... && staticcheck ./... && go test ./...`.
