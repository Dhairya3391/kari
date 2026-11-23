package config

const (
	MovyReferer      = "https://www.movy.sx/"
	TMDBAPIBase      = "https://api.themoviedb.org/3"
	TMDBBaseURL      = "https://www.themoviedb.org"
	TMDBImageBase    = "https://image.tmdb.org/t/p/"
	DesktopUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"
	AndroidUAConst   = "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Mobile Safari/537.36"
	AppName          = "Kari"
	OpenSubtitlesAPI = "https://api.opensubtitles.com/api/v1"
	OpenSubtitlesUA  = "kari v1.0"
	SearchAPIBase    = "https://noobsearch.broggl.farm"
	VylaAPIBase      = "https://noob.broggl.farm"
	KariClientHeader = "X-Kari-Client"
	KariClientToken  = "kari-app-v1"
	TraktAPIBase     = "https://api.trakt.tv"
	TraktAuthBase    = "https://trakt.tv"
	AniListAPIBase   = "https://graphql.anilist.co"
	AniListAuthBase  = "https://anilist.co"
	// AniListReferer is sent when downloading AniList-hosted artwork so the
	// image CDN serves the real cover instead of a hotlink placeholder.
	AniListReferer = "https://anilist.co/"

	DefaultTraktClientID       = "3638fff997ff24c5deb51646a7520bc531f5d482a30def91c238a8d2a0bd648d"
	DefaultTraktClientSecret   = "3aeb56d02d938833b0395ddd484b9c13849b0523bb32d0b0635cb9b048eed91d"
	DefaultAniListClientID     = "40882"
	DefaultAniListClientSecret = "g25ntjZ1mQeHVtHjNZeGpLyVidaA2xkMFQwsmYKu"

	// AnimeSkipAPIBase is the Anime-Skip GraphQL endpoint.
	AnimeSkipAPIBase = "https://api.anime-skip.com/graphql"
	// AnimeSkipRESTBase is the legacy Anime-Skip REST root used for OP/ED times.
	AnimeSkipRESTBase = "https://api.aniskip.com/v1"
	// GitHubAPIBase is the GitHub REST root used by the self-updater.
	GitHubAPIBase = "https://api.github.com"
	// DefaultAnimeSkipClientID is the bundled X-Client-ID for the Anime-Skip API.
	DefaultAnimeSkipClientID = "q5ZuG3MoJxy3c4dKJvpOjFIh1HFGTsrm"

	// SkipDBAPIBase is the SkipDB API base endpoint.
	SkipDBAPIBase = "https://api.skipdb.tv"
	// DefaultSkipDBAPIKey is the hardcoded API key for SkipDB.
	DefaultSkipDBAPIKey = "skdb_lBMr8tAJgeEmtebM155UK9-HscdeJDgC"

	// IntroDBAPIBase is the IntroDB API base endpoint.
	IntroDBAPIBase = "https://api.introdb.app"
	// DefaultIntroDBAPIKey is the hardcoded API key for IntroDB.
	DefaultIntroDBAPIKey = "idb_Cbx3JSk4-MLeS8iOXQGFY0hXvM06XdFs"

	// WeebCentralBase is the WeebCentral site (HTMX HTML, no API):
	// series search, full chapter listings, and chapter page images.
	// Shapes were verified live; polite throttling applies per client.
	WeebCentralBase = "https://weebcentral.com"
	// WeebCentralReferer satisfies the image CDN's hotlink protection
	// on chapter page downloads.
	WeebCentralReferer = "https://weebcentral.com/"

	// MiruroBase is the Miruro anime site (SvelteKit, no public API):
	// search, episode listings, and per-episode streams all come from
	// its __data.json endpoints. The miruro.cx host is the current
	// SvelteKit deployment while www.miruro.cx hosts the React/API v1 SPA.
	MiruroBase = "https://miruro.cx"

	// AniVaultAPIBase exposes the current site's public Hoshika catalog and
	// playback routes without requiring the private scraper's API key.
	AniVaultAPIBase = "https://www.anivault.co"

	// SolisAPIBase is the hosted streaming resolution API deployed on Vercel.
	SolisAPIBase = "https://solis-api.vercel.app"

	// CnoBase is the main web and API base for Cno (https://cno.to).
	CnoBase = "https://cno.to"
	// CnoReferer satisfies hotlink and header protection on Cno API and stream requests.
	CnoReferer = "https://cno.to/"

	// Q62Base is the Q62 web application (https://q62flix.ws).
	Q62Base = "https://q62flix.ws"
	// Q62APIBase serves Q62's catalog and signed release descriptors. Content
	// is keyed by opaque site IDs rather than TMDB IDs, so search and episode
	// listings go through this API instead of the shared TMDB-backed base.
	Q62APIBase = "https://api.q62flix.ws"
	// Q62StreamProxyBase is the media proxy referenced by the site's own
	// player. Release descriptors carry relative HLS/VTT paths with a signed
	// token; the proxy is the only host that serves them.
	Q62StreamProxyBase = "https://proxy.7movie.ws"

	// CinejoyBase is the Cinejoy web application (https://cinejoy.pk). Its
	// sealed resolution gateway and stream CDNs both key on this origin, so
	// sources carry it as their Referer and derived Origin header.
	CinejoyBase = "https://cinejoy.pk"
	// CinejoyAPIBase is Cinejoy's sealed resolution gateway. Every request is
	// an HPKE-style ECDH + AES-GCM envelope (see internal/provider/cinejoy).
	CinejoyAPIBase = "https://api2.wing.st"
	// CinecatBase is the public web application for Cinecat (https://beta.cinecat.eu).
	CinecatBase = "https://beta.cinecat.eu"
	// CinecatReferer satisfies the hotlink protection on Cinecat stream requests.
	CinecatReferer = "https://beta.cinecat.eu/"
	// CinecatCDNBase is the current ZCDN edge for Cinecat's adaptive HLS lane.
	// The site's /config.js picks any of zcdn1-3 on this workers.dev deployment;
	// the former c3204a347 hosts were retired and now answer 429. The new edge
	// (and the sources API) additionally require an x-cap-pass header obtained
	// from a cap.js proof-of-work; without it these lanes answer 401. The
	// Aphrodite lane below is ungated and remains the primary playback source.
	CinecatCDNBase = "https://zcdn1.e6dd8d160-edge.workers.dev"
	// CinecatSourcesAPIBase is Cinecat's multi-lane sources API. It lists the
	// available streaming backends and resolves each one by TMDB coordinates.
	CinecatSourcesAPIBase = "https://edge.cinecat.eu"
	// CinecatAphroditeBase is Cinecat's self-hosted Aphrodite content lookup.
	// It resolves a TMDB title straight to its HLS master on the totallyacdn.cc
	// edge, independently of the ZCDN token API.
	CinecatAphroditeBase = "https://cdn.hls.lol"
	CinecatSubsBase      = "https://sub.vdrk.site"

	// VumetoBase is the Vumeto web application (https://vumeto.live). Its
	// /api catalog serves TMDB-keyed search, title, and season payloads for
	// movies, TV series, and cartoons.
	VumetoBase = "https://vumeto.live"
	// VumetoPlayerBase is Vumeto's embed player origin. Its /_player/resolve
	// endpoint returns the player's direct stream lanes; the site origin is
	// sent as the session's parentOrigin.
	VumetoPlayerBase = "https://player.vumeto.live"

	// VidGoldPlayerBase is the embed player origin VidGold uses. The player
	// cascades over seven internal stream lanes (Aurum, Sovereign, Bullion,
	// Ingot, Nugget, Doubloon, Gilt).
	VidGoldPlayerBase = "https://player.vidgold.net"
	// VidGoldAPIBase is the embed player's JSON resolution API. Every lane is
	// addressed through it with the TMDB id plus a sources key (s1–s7).
	VidGoldAPIBase = "https://api.vidgold.net"

	// VidoraBase is the embed player origin for Vidora (https://vidora.ac).
	// It serves no API; the player resolves through the hls.lol lanes below
	// and only needs this origin as the hotlink Referer.
	VidoraBase = "https://vidora.ac"
	// VidoraContentBase is Vidora's Aphrodite content edge, a TMDB-keyed
	// lookup that returns a signed HLS master on the totallyacdn.cc CDN.
	VidoraContentBase = "https://cdn.hls.lol"
	// VidoraStreamBase serves the Vidbox and Prism (mbox) player lanes.
	VidoraStreamBase = "https://stream.hls.lol"
	// VidoraSheguBase is Vidora's Shegu resolver, which returns a direct
	// signed HLS master for a TMDB coordinate.
	VidoraSheguBase = "https://shegu.hls.lol"
	// VidoraPalBase serves Bandit's per-server stream listing.
	VidoraPalBase = "https://pal.hls.lol"
	// VidoraSubsBase is Vidora's subtitle lookup service.
	VidoraSubsBase = "https://natsuki.hls.lol"

	// MappleBase is the main web and API base for Mapple (https://mapple.fun).
	MappleBase = "https://mapple.fun"
	// MappleReferer satisfies hotlink and header protection on Mapple API and stream requests.
	MappleReferer = "https://mapple.fun/"
	// VidRiftSiteBase is the VidRift landing site used as the embed request
	// referer. VidRiftEmbedBase is the TMDB/AniList-keyed player origin that
	// serves both the embed document and its /api/source lanes.
	VidRiftSiteBase  = "https://vidrift.net"
	VidRiftEmbedBase = "https://embed.vidrift.net"

	// LightflixBase is the Lightflix web application (https://lightflix.app).
	// Its playback API issues short-lived, IP-bound stream tokens through a
	// proof-of-work handshake; the site origin is also the API Referer.
	LightflixBase = "https://lightflix.app"

	// AniDreamBase is the AniDream web application (https://anidream.to).
	// It is a TanStack Start SPA whose catalog, playback, and reader data
	// are served by hashed /_serverFn endpoints.
	AniDreamBase = "https://anidream.to"
	// AniDreamReferer is sent on server-function and media requests; the
	// upstream gateway keys its session on the site origin.
	AniDreamReferer = "https://anidream.to/"

	// BingrSiteBase is Bingr's current web application (https://bingr.one).
	// Its watch pages embed a short-lived sealed payload holding the streams
	// that replaced the retired shard API on api.bingr.st.
	BingrSiteBase = "https://bingr.one"
	// DefaultDiscordAppID is the Kari Discord application used for Rich
	// Presence. Its registered name is what users see as the activity verb
	// ("Watching Kari"); the KARI_DISCORD_APP_ID env var overrides it for
	// forks and development.
	DefaultDiscordAppID = "1560260983619059743"
	// DiscordAssetLogo and DiscordAssetPaused are Rich Presence asset keys
	// uploaded under the Discord application. Per-title posters are sent as
	// external URLs instead, so only these two app-level assets exist.
	DiscordAssetLogo   = "kari_logo"
	DiscordAssetPaused = "paused"
)

// Famelack live TV catalog endpoints. Famelack (formerly TV.Garden) publishes
// its directory as JSON from its public data host.
const (
	// FamelackDataBase is the root of Famelack's public data host.
	FamelackDataBase = "https://data.famelack.com"
	// FamelackTVChannelsURL is the complete live TV channel catalog.
	FamelackTVChannelsURL = FamelackDataBase + "/tv/categories/all.json"
	// FamelackTVCountriesURL maps ISO country codes to display names.
	FamelackTVCountriesURL = FamelackDataBase + "/tv/countries_metadata.json"
)

// DefaultTMDBAPIKeys are the bundled fallback keys; TMDB_API_KEY env var
// replaces the whole list when set.
var DefaultTMDBAPIKeys = []string{
	"fb7bb23f03b6994dafc674c074d01761",
	"e55425032d3d0f371fc776f302e7c09b",
	"8301a21598f8b45668d5711a814f01f6",
	"8cf43ad9c085135b9479ad5cf6bbcbda",
	"da63548086e399ffc910fbc08526df05",
	"13e53ff644a8bd4ba37b3e1044ad24f3",
	"269890f657dddf4635473cf4cf456576",
	"a2f888b27315e62e471b2d587048f32e",
	"8476a7ab80ad76f0936744df0430e67c",
	"5622cafbfe8f8cfe358a29c53e19bba0",
	"ae4bd1b6fce2a5648671bfc171d15ba4",
	"257654f35e3dff105574f97fb4b97035",
	"2f4038e83265214a0dcd6ec2eb3276f5",
	"9e43f45f94705cc8e1d5a0400d19a7b7",
	"af6887753365e14160254ac7f4345dd2",
	"06f10fc8741a672af455421c239a1ffc",
	"09ad8ace66eec34302943272db0e8d2c",
}
