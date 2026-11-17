package thedaddy

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kari/internal/httpclient"
	"kari/internal/provider"
)

// guideChannel is one 24/7 channel or one channel carrying an event.
type guideChannel struct {
	ID   int
	Name string
}

// guideEvent is one scheduled sports or TV event.
type guideEvent struct {
	ID       string
	Title    string
	Category string
	Section  string
	StartsAt time.Time
	Live     bool
	Channels []guideChannel
}

// liveGuide is the scraped schedule plus the 24/7 channel catalog.
type liveGuide struct {
	Events   []guideEvent
	Channels []guideChannel
	Warnings []string
}

const (
	// liveWindow is how long an event stays marked live after its start time.
	// The directory lists start times only, so events with long run times are
	// treated as live for this window instead of ending immediately.
	liveWindow = 4 * time.Hour
	// scrapedAtLayout is used only for cache bookkeeping in logs.
	scrapedAtLayout = time.RFC3339
)

var (
	reSectionSplit    = regexp.MustCompile(`(?i)<section[ >]`)
	reSectionTitle    = regexp.MustCompile(`(?is)<h2[^>]*>\s*([^<]+)`)
	reEndpoint        = regexp.MustCompile(`data-endpoint="([^"]+)"`)
	reDaySplit        = regexp.MustCompile(`<div class="schedule__day"`)
	reDayTitle        = regexp.MustCompile(`(?is)<div class="schedule__dayTitle">(.*?)</div>`)
	reCategorySplit   = regexp.MustCompile(`<div class="schedule__category["> ]`)
	reCategoryTitle   = regexp.MustCompile(`(?is)<div class="card__meta">(.*?)</div>`)
	reEventSplit      = regexp.MustCompile(`<div class="schedule__event">`)
	reEventTime       = regexp.MustCompile(`class="schedule__time"\s+data-time="([^"]+)"`)
	reEventTitle      = regexp.MustCompile(`(?is)class="schedule__eventTitle">(.*?)</span>`)
	reEventChannel    = regexp.MustCompile(`(?i)<a[^>]+href="/watch\.php\?id=(\d+)"[^>]*>([^<]*)</a>`)
	reChannelCard     = regexp.MustCompile(`(?is)<a class="card"[^>]*href="/watch\.php\?id=(\d+)"[^>]*>(.*?)</a>`)
	reChannelCardName = regexp.MustCompile(`(?is)<div class="card__title">(.*?)</div>`)
	reTags            = regexp.MustCompile(`<[^>]+>`)
	reSpace           = regexp.MustCompile(`\s+`)
	reDateDMY         = regexp.MustCompile(`(\d{1,2})(?:st|nd|rd|th)?\s+([A-Za-z]{3,9})\.?,?\s+(\d{4})`)
	reDateMDY         = regexp.MustCompile(`([A-Za-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})`)
)

var monthNames = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

// scrapeGuide fetches the homepage schedule, its remotely loaded extra
// sections, and the 24/7 channel catalog. Partial failures are reported as
// warnings so one dead section does not hide the rest of the guide.
func (c *Client) scrapeGuide(ctx context.Context) (*liveGuide, error) {
	homeURL := c.baseURL + "/"
	var (
		home     string
		homeErr  error
		channels []guideChannel
		chanErr  error
		wg       sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		home, homeErr = c.fetchText(ctx, homeURL, "")
	}()
	go func() {
		defer wg.Done()
		channels, chanErr = c.scrapeChannels(ctx)
	}()
	wg.Wait()
	if homeErr != nil {
		return nil, fmt.Errorf("scrape homepage: %w", homeErr)
	}

	guide := &liveGuide{Channels: channels}
	if chanErr != nil {
		guide.Warnings = append(guide.Warnings, fmt.Sprintf("channels: %v", chanErr))
	}

	events, warnings := c.scrapeHomeSchedule(ctx, homeURL, home)
	guide.Warnings = append(guide.Warnings, warnings...)
	guide.Events = events
	guide.dedupe()
	markLive(guide.Events, time.Now())
	thedaddyLog.Debug("guide scraped",
		"events", len(guide.Events),
		"channels", len(guide.Channels),
		"warnings", len(guide.Warnings),
		"at", time.Now().Format(scrapedAtLayout))
	return guide, nil
}

// scrapeHomeSchedule parses the inline schedule and fetches every section that
// loads its days from schedule-api.php.
func (c *Client) scrapeHomeSchedule(ctx context.Context, homeURL, home string) ([]guideEvent, []string) {
	sections := splitSections(home)
	type remote struct {
		title    string
		endpoint string
	}
	var (
		events   []guideEvent
		warnings []string
		remotes  []remote
	)
	for _, section := range sections {
		title := cleanText(firstGroup(reSectionTitle, section))
		if endpoint := firstGroup(reEndpoint, section); endpoint != "" {
			if title == "" {
				title = "Extra Schedule"
			}
			remotes = append(remotes, remote{title: title, endpoint: endpoint})
			continue
		}
		events = append(events, parseScheduleHTML(section, title)...)
	}

	// The extra sections are third-party schedules. Fetch them with a small
	// amount of parallelism and keep whatever succeeds.
	type result struct {
		title  string
		events []guideEvent
		err    error
	}
	results := make([]result, 0, len(remotes))
	mu := sync.Mutex{}
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, item := range remotes {
		wg.Add(1)
		go func(item remote) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			events, err := c.fetchRemoteSchedule(ctx, homeURL, item.endpoint, item.title)
			mu.Lock()
			results = append(results, result{title: item.title, events: events, err: err})
			mu.Unlock()
		}(item)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool { return results[i].title < results[j].title })
	for _, item := range results {
		if item.err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", item.title, item.err))
			continue
		}
		events = append(events, item.events...)
	}
	return events, warnings
}

// fetchRemoteSchedule loads one schedule-api.php section and parses its HTML.
func (c *Client) fetchRemoteSchedule(ctx context.Context, homeURL, endpoint, title string) ([]guideEvent, error) {
	target := endpoint
	if strings.HasPrefix(target, "/") {
		target = c.baseURL + target
	}
	body, err := c.fetchText(ctx, target, homeURL)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, fmt.Errorf("decode schedule: %w", err)
	}
	if !payload.Success {
		return nil, fmt.Errorf("schedule endpoint reported failure")
	}
	if strings.Contains(payload.HTML, "No extra schedule") {
		return nil, nil
	}
	return parseScheduleHTML(payload.HTML, title), nil
}

// scrapeChannels loads the 24/7 channel catalog.
func (c *Client) scrapeChannels(ctx context.Context) ([]guideChannel, error) {
	body, err := c.fetchText(ctx, c.baseURL+"/24-7-channels.php", c.baseURL+"/")
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{})
	channels := make([]guideChannel, 0, 256)
	for _, card := range reChannelCard.FindAllStringSubmatch(body, -1) {
		id, err := strconv.Atoi(card[1])
		if err != nil || id <= 0 {
			continue
		}
		name := cleanText(firstGroup(reChannelCardName, card[2]))
		if name == "" {
			name = fmt.Sprintf("Channel %d", id)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		channels = append(channels, guideChannel{ID: id, Name: name})
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("no channels found: %w", provider.ErrUpstreamChanged)
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].Name < channels[j].Name })
	return channels, nil
}

// parseScheduleHTML parses every day in one schedule fragment. The fragment is
// either the homepage body or the HTML returned by schedule-api.php.
func parseScheduleHTML(fragment, section string) []guideEvent {
	today := time.Now()
	events := make([]guideEvent, 0, 64)
	for _, day := range splitMarked(fragment, reDaySplit) {
		title := cleanText(firstGroup(reDayTitle, day))
		date, ok := parseScheduleDate(title, today)
		for _, category := range splitMarked(day, reCategorySplit) {
			categoryName := cleanText(firstGroup(reCategoryTitle, category))
			if categoryName == "" {
				categoryName = "Events"
			}
			for _, block := range splitMarked(category, reEventSplit) {
				startTime := firstGroup(reEventTime, block)
				titleText := cleanText(firstGroup(reEventTitle, block))
				if titleText == "" || startTime == "" {
					continue
				}
				channels := parseEventChannels(block)
				if len(channels) == 0 {
					continue
				}
				event := guideEvent{
					ID:       eventHandle(channels),
					Title:    titleText,
					Category: categoryName,
					Section:  section,
					Channels: channels,
				}
				if ok {
					event.StartsAt = scheduleTime(date, startTime)
				}
				events = append(events, event)
			}
		}
	}
	return events
}

// parseEventChannels extracts the channel links listed under one event.
func parseEventChannels(block string) []guideChannel {
	seen := make(map[int]struct{})
	channels := make([]guideChannel, 0, 4)
	for _, match := range reEventChannel.FindAllStringSubmatch(block, -1) {
		id, err := strconv.Atoi(match[1])
		if err != nil || id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		name := cleanText(match[2])
		if name == "" {
			name = fmt.Sprintf("Channel %d", id)
		}
		channels = append(channels, guideChannel{ID: id, Name: name})
	}
	return channels
}

// parseScheduleDate reads the day heading, which looks like
// "Tuesday 17th Nov 2026 - Schedule Time UK GMT".
func parseScheduleDate(title string, now time.Time) (time.Time, bool) {
	if match := reDateDMY.FindStringSubmatch(title); match != nil {
		return makeDate(match[3], match[2], match[1])
	}
	if match := reDateMDY.FindStringSubmatch(title); match != nil {
		return makeDate(match[3], match[1], match[2])
	}
	return now, false
}

// makeDate builds a date and guards against absurd scraped values.
func makeDate(yearText, monthText, dayText string) (time.Time, bool) {
	year, err := strconv.Atoi(yearText)
	if err != nil || year < 2000 || year > 2100 {
		return time.Time{}, false
	}
	month, ok := monthNames[strings.ToLower(strings.TrimSuffix(monthText, "."))]
	if !ok {
		return time.Time{}, false
	}
	day, err := strconv.Atoi(dayText)
	if err != nil || day < 1 || day > 31 {
		return time.Time{}, false
	}
	return time.Date(year, month, day, 0, 0, 0, 0, scheduleLocation()), true
}

// scheduleTime combines a schedule date with a "15:30" UK wall-clock time.
// The site labels its times "UK GMT", but it follows the Europe/London clock
// (GMT in winter, BST in summer), so the location is resolved properly.
func scheduleTime(date time.Time, clock string) time.Time {
	parts := strings.Split(strings.TrimSpace(clock), ":")
	if len(parts) < 2 {
		return date
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil {
		return date
	}
	loc := scheduleLocation()
	return time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
}

// scheduleLocation returns Europe/London, falling back to UTC when the host has
// no tzdata installed (winter times then still line up with the site's label).
func scheduleLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		return time.UTC
	}
	return loc
}

// markLive flags events that started within the last liveWindow.
func markLive(events []guideEvent, now time.Time) {
	for i := range events {
		start := events[i].StartsAt
		if start.IsZero() {
			continue
		}
		events[i].Live = !now.Before(start) && now.Before(start.Add(liveWindow))
	}
}

// dedupe collapses the same event listed twice (for example a homepage card
// and an extra schedule entry) while keeping every distinct channel.
func (g *liveGuide) dedupe() {
	index := make(map[string]int, len(g.Events))
	merged := make([]guideEvent, 0, len(g.Events))
	for _, event := range g.Events {
		key := strings.ToLower(event.Title) + "|" + event.StartsAt.UTC().Format(scrapedAtLayout)
		if pos, ok := index[key]; ok {
			merged[pos].Channels = mergeChannels(merged[pos].Channels, event.Channels)
			merged[pos].ID = eventHandle(merged[pos].Channels)
			continue
		}
		index[key] = len(merged)
		merged = append(merged, event)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].StartsAt.Equal(merged[j].StartsAt) {
			return merged[i].Title < merged[j].Title
		}
		if merged[i].StartsAt.IsZero() {
			return false
		}
		if merged[j].StartsAt.IsZero() {
			return true
		}
		return merged[i].StartsAt.Before(merged[j].StartsAt)
	})
	g.Events = merged
}

// mergeChannels appends channels that are not already present.
func mergeChannels(base, extra []guideChannel) []guideChannel {
	seen := make(map[int]struct{}, len(base)+len(extra))
	for _, channel := range base {
		seen[channel.ID] = struct{}{}
	}
	for _, channel := range extra {
		if _, dup := seen[channel.ID]; dup {
			continue
		}
		seen[channel.ID] = struct{}{}
		base = append(base, channel)
	}
	return base
}

// fetchText performs a GET with retries; the directory resets connections
// under load, so a retry usually succeeds.
func (c *Client) fetchText(ctx context.Context, target, referer string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json")
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := httpclient.ReadCapped(resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = &provider.HTTPError{Code: resp.StatusCode, URL: target}
			continue
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return string(body), nil
	}
	return "", lastErr
}

// splitSections splits the homepage into its <section> blocks.
func splitSections(page string) []string {
	indexes := reSectionSplit.FindAllStringIndex(page, -1)
	sections := make([]string, 0, len(indexes))
	for i, loc := range indexes {
		end := len(page)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		sections = append(sections, page[loc[0]:end])
	}
	return sections
}

// splitMarked splits block on every match of marker, keeping the marker at the
// start of each returned piece.
func splitMarked(block string, marker *regexp.Regexp) []string {
	indexes := marker.FindAllStringIndex(block, -1)
	pieces := make([]string, 0, len(indexes))
	for i, loc := range indexes {
		end := len(block)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		pieces = append(pieces, block[loc[0]:end])
	}
	return pieces
}

// firstGroup returns the first capture group of the first match, or "".
func firstGroup(re *regexp.Regexp, text string) string {
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// cleanText strips tags and entities and collapses whitespace.
func cleanText(value string) string {
	value = reTags.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.TrimSpace(reSpace.ReplaceAllString(value, " "))
}
