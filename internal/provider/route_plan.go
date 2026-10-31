package provider

// RouteStrategy defines how an operation consumes its ordered routes.
type RouteStrategy string

const (
	// RouteFanOut runs all routes together and combines usable results.
	RouteFanOut RouteStrategy = "fan-out"
	// RouteFallback tries routes in priority order until one succeeds.
	RouteFallback RouteStrategy = "fallback"
	// RouteSingle runs the only route available for the operation.
	RouteSingle RouteStrategy = "single"
)

// Route is one provider's declared place in a mode's execution order.
type Route struct {
	Provider  Provider
	Priority  int
	Deadlines Deadlines
	Playback  EpisodeSource
	Manga     MangaSource
}

// RoutePlan groups the registered providers that can serve each stage of a
// mode. Every route follows provider mode priority; no provider-name table is
// needed to add a new integration.
type RoutePlan struct {
	Mode             ContentType
	SearchStrategy   RouteStrategy
	PlaybackStrategy RouteStrategy
	MangaStrategy    RouteStrategy
	Search           []Route
	Playback         []Route
	Manga            []Route
}
