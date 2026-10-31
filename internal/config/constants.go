package config

const (
	AnikotoBase      = "https://anikototv.to"
	MovyReferer      = "https://www.movy.sx/"
	PenguAPIBase     = "https://pengu.uk"
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

	DefaultTraktClientID       = "3638fff997ff24c5deb51646a7520bc531f5d482a30def91c238a8d2a0bd648d"
	DefaultTraktClientSecret   = "3aeb56d02d938833b0395ddd484b9c13849b0523bb32d0b0635cb9b048eed91d"
	DefaultAniListClientID     = "40882"
	DefaultAniListClientSecret = "g25ntjZ1mQeHVtHjNZeGpLyVidaA2xkMFQwsmYKu"

	// AnimeSkipAPIBase is the Anime-Skip GraphQL endpoint.
	AnimeSkipAPIBase = "https://api.anime-skip.com/graphql"
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

	// DefaultPenguAuthToken is the bundled fallback token for PenguPlay (https://pengu.uk).
	DefaultPenguAuthToken = "fJDLLgJTS0ASMne0HsUILe4284c5A_FlRKI9xW8qYpk"

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

	// AniVaultAPIBase is AniVault's public scraper API. The anivault.tv
	// frontend is Cloudflare-challenged for non-browser clients; this
	// deployment serves the same AniList-keyed catalog and playback routes.
	AniVaultAPIBase = "https://anivault-api-production.up.railway.app"

	// AniPMBase is the public web application for Ani.pm (https://ani.pm).
	AniPMBase = "https://ani.pm"
	// AniPMReferer satisfies hotlink protection on Ani.pm API requests.
	AniPMReferer = "https://ani.pm/"
	// AniPMEmbedBase is the Settlar embed host for Ani.pm streams.
	AniPMEmbedBase = "https://embed.settlar.io"
	// AniPMMediaReferer satisfies hotlink protection on media.settlar.io stream requests.
	AniPMMediaReferer = "https://embed.settlar.io/"

	// SolisAPIBase is the hosted streaming resolution API deployed on Vercel.
	SolisAPIBase = "https://solis-api.vercel.app"
	// CinecatBase is the public web application for Cinecat (https://beta.cinecat.eu).
	CinecatBase = "https://beta.cinecat.eu"
	// CinecatReferer satisfies the hotlink protection on Cinecat stream requests.
	CinecatReferer = "https://beta.cinecat.eu/"
	// CinecatCDNBase is the primary multi-quality HLS streaming backend for Cinecat.
	CinecatCDNBase  = "https://zcdn1.iamwatchingyou.site"
	CinecatSubsBase = "https://sub.vdrk.site"

	// CnoBase is the main web and API base for Cno (https://cno.to).
	CnoBase = "https://cno.to"
	// CnoReferer satisfies hotlink and header protection on Cno API and stream requests.
	CnoReferer = "https://cno.to/"

	// MappleBase is the main web and API base for Mapple (https://mapple.fun).
	MappleBase = "https://mapple.fun"
	// MappleReferer satisfies hotlink and header protection on Mapple API and stream requests.
	MappleReferer = "https://mapple.fun/"
	// VidRiftSiteBase is the VidRift landing site used as the embed request
	// referer. VidRiftEmbedBase is the TMDB/AniList-keyed player origin that
	// serves both the embed document and its /api/source lanes.
	VidRiftSiteBase  = "https://vidrift.net"
	VidRiftEmbedBase = "https://embed.vidrift.net"

	// BingrAPIBase is the REST API endpoint for Bingr (https://bingr.one).
	BingrAPIBase = "https://api.bingr.one/api"
	// BingrOrigin is the Origin header sent to Bingr API endpoints.
	BingrOrigin = "https://bingr.one"
	// BingrReferer satisfies hotlink and referer requirements on Bingr API requests.
	BingrReferer = "https://bingr.one/"
	// FilmuAPIBase is the HiAnime/MegaPlay token and stream gateway used by Bingr for anime.
	FilmuAPIBase = "https://hianime.filmu.in"
	// MegaPlayReferer satisfies hotlink protection on MegaPlay stream and subtitle requests.
	MegaPlayReferer = "https://megaplay.buzz/"
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
