package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/models"
	"novastream/services/calendar"
)

const (
	defaultTitleTemplate = "{{eventLabel}}: {{title}}"
	defaultBodyTemplate  = "{{mediaLabel}}{{progressLabel}}{{releaseLabel}}"
)

var validEvents = map[string]bool{
	models.NotificationEventWatchStarted: true,
	models.NotificationEventWatchPlaying: true,
	models.NotificationEventWatchResumed: true,
	models.NotificationEventWatchWatched: true,
	models.NotificationEventRelease:      true,
}

type delivery struct {
	channel models.NotificationChannel
	event   models.NotificationEvent
}

type playbackSession struct {
	seen       bool
	activeSeen bool
	paused     bool
	watched    bool
	updatedAt  time.Time
}

// Service owns profile notification configuration, formatting, and delivery.
type Service struct {
	repo       datastore.NotificationRepository
	httpClient *http.Client
	deliveries chan delivery
	stop       chan struct{}

	sessionMu   sync.Mutex
	sessions    map[string]playbackSession
	observeMu   sync.Mutex
	deliveredMu sync.Mutex
	delivered   map[string]time.Time
}

func New(repo datastore.NotificationRepository) *Service {
	s := &Service{
		repo: repo,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		deliveries: make(chan delivery, 256),
		stop:       make(chan struct{}),
		sessions:   make(map[string]playbackSession),
		delivered:  make(map[string]time.Time),
	}
	go s.run()
	return s
}

func (s *Service) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *Service) ListChannels(ctx context.Context, profileID string) ([]models.NotificationChannel, error) {
	channels, err := s.repo.ListChannels(ctx, strings.TrimSpace(profileID))
	if err != nil {
		return nil, err
	}
	for i := range channels {
		channels[i].URLConfigured = channels[i].URL != ""
		channels[i].URL = ""
	}
	return channels, nil
}

func (s *Service) SaveChannel(ctx context.Context, channel models.NotificationChannel) (models.NotificationChannel, error) {
	channel.ID = strings.TrimSpace(channel.ID)
	channel.ProfileID = strings.TrimSpace(channel.ProfileID)
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Type = strings.ToLower(strings.TrimSpace(channel.Type))
	channel.URL = strings.TrimSpace(channel.URL)
	channel.TitleTemplate = strings.TrimSpace(channel.TitleTemplate)
	channel.BodyTemplate = strings.TrimSpace(channel.BodyTemplate)

	if channel.ProfileID == "" {
		return models.NotificationChannel{}, errors.New("profile ID is required")
	}
	if channel.Name == "" {
		return models.NotificationChannel{}, errors.New("name is required")
	}
	if channel.Type != models.NotificationChannelDiscord && channel.Type != models.NotificationChannelWebhook {
		return models.NotificationChannel{}, errors.New("type must be discord or webhook")
	}
	channel.Events = normalizeEvents(channel.Events)
	if len(channel.Events) == 0 {
		return models.NotificationChannel{}, errors.New("select at least one event")
	}
	hasReleaseEvents := contains(channel.Events, models.NotificationEventRelease)
	if hasReleaseEvents && len(channel.Events) > 1 {
		return models.NotificationChannel{}, errors.New("watch status and release status events must use separate destinations")
	}
	if hasReleaseEvents {
		if !channel.NotifyWatchlist && !channel.NotifyTrending {
			return models.NotificationChannel{}, errors.New("select at least one release source")
		}
	} else {
		channel.NotifyWatchlist = false
		channel.NotifyTrending = false
	}
	if channel.TrendingLimit < 1 {
		channel.TrendingLimit = 20
	}
	if channel.TrendingLimit > 100 {
		channel.TrendingLimit = 100
	}
	if channel.TitleTemplate == "" {
		channel.TitleTemplate = defaultTitleTemplate
	}
	if channel.BodyTemplate == "" {
		channel.BodyTemplate = defaultBodyTemplate
	}

	now := time.Now().UTC()
	if channel.ID == "" {
		if err := validateDestination(channel.Type, channel.URL); err != nil {
			return models.NotificationChannel{}, err
		}
		channel.ID = uuid.NewString()
		channel.CreatedAt = now
		channel.UpdatedAt = now
		channel.URLConfigured = true
		if err := s.repo.CreateChannel(ctx, &channel); err != nil {
			return models.NotificationChannel{}, fmt.Errorf("create notification channel: %w", err)
		}
	} else {
		existing, err := s.repo.GetChannel(ctx, channel.ID)
		if err != nil {
			return models.NotificationChannel{}, fmt.Errorf("load notification channel: %w", err)
		}
		if existing == nil || existing.ProfileID != channel.ProfileID {
			return models.NotificationChannel{}, errors.New("notification channel not found")
		}
		if channel.URL == "" {
			channel.URL = existing.URL
		}
		if err := validateDestination(channel.Type, channel.URL); err != nil {
			return models.NotificationChannel{}, err
		}
		channel.CreatedAt = existing.CreatedAt
		channel.UpdatedAt = now
		channel.URLConfigured = channel.URL != ""
		if err := s.repo.UpdateChannel(ctx, &channel); err != nil {
			return models.NotificationChannel{}, fmt.Errorf("update notification channel: %w", err)
		}
	}

	channel.URL = ""
	return channel, nil
}

func (s *Service) DeleteChannel(ctx context.Context, profileID, id string) error {
	return s.repo.DeleteChannel(ctx, strings.TrimSpace(profileID), strings.TrimSpace(id))
}

func (s *Service) TestChannel(ctx context.Context, profileID, id string) error {
	channel, err := s.repo.GetChannel(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if channel == nil || channel.ProfileID != strings.TrimSpace(profileID) {
		return errors.New("notification channel not found")
	}
	event := models.NotificationEvent{
		ID:         uuid.NewString(),
		Type:       models.NotificationEventWatchPlaying,
		ProfileID:  channel.ProfileID,
		Title:      "Notification test",
		MediaType:  "movie",
		Percent:    42,
		OccurredAt: time.Now().UTC(),
	}
	if contains(channel.Events, models.NotificationEventRelease) {
		event.Type = models.NotificationEventRelease
		event.ReleaseType = "digital"
		event.ReleaseDate = event.OccurredAt.Format("2006-01-02")
		event.Source = "test"
	}
	return s.deliver(ctx, *channel, event)
}

// HandlePlaybackUpdate converts player heartbeats into edge-triggered watch events.
func (s *Service) HandlePlaybackUpdate(userID string, update models.PlaybackProgressUpdate, percent float64) {
	if update.MediaType == "live" {
		return
	}
	key := userID + "\x00" + update.MediaType + "\x00" + update.ItemID
	now := time.Now().UTC()
	active := !update.IsPaused && !update.IsBuffering

	s.sessionMu.Lock()
	state := s.sessions[key]
	if !state.updatedAt.IsZero() && now.Sub(state.updatedAt) > 30*time.Minute {
		state = playbackSession{}
	}
	var eventTypes []string
	if !state.seen {
		eventTypes = append(eventTypes, models.NotificationEventWatchStarted)
		state.seen = true
	}
	if active && !state.activeSeen {
		eventTypes = append(eventTypes, models.NotificationEventWatchPlaying)
		state.activeSeen = true
	} else if active && state.paused {
		eventTypes = append(eventTypes, models.NotificationEventWatchResumed)
	}
	if percent >= 90 && !state.watched {
		eventTypes = append(eventTypes, models.NotificationEventWatchWatched)
		state.watched = true
	}
	state.paused = update.IsPaused || update.IsBuffering
	state.updatedAt = now
	if update.PlaybackEnded {
		delete(s.sessions, key)
	} else {
		s.sessions[key] = state
	}
	s.pruneSessionsLocked(now)
	s.sessionMu.Unlock()

	for _, eventType := range eventTypes {
		s.Notify(models.NotificationEvent{
			ID:            uuid.NewString(),
			Type:          eventType,
			ProfileID:     userID,
			Title:         playbackTitle(update),
			MediaType:     update.MediaType,
			Year:          update.Year,
			SeriesTitle:   update.SeriesName,
			EpisodeTitle:  update.EpisodeName,
			SeasonNumber:  update.SeasonNumber,
			EpisodeNumber: update.EpisodeNumber,
			Position:      update.Position,
			Duration:      update.Duration,
			Percent:       percent,
			PosterURL:     update.PosterURL,
			ExternalIDs:   update.ExternalIDs,
			OccurredAt:    now,
		})
	}
}

func (s *Service) pruneSessionsLocked(now time.Time) {
	if len(s.sessions) < 256 {
		return
	}
	for key, state := range s.sessions {
		if now.Sub(state.updatedAt) > 30*time.Minute {
			delete(s.sessions, key)
		}
	}
}

// ObserveCalendar establishes a baseline, then emits release events when an
// already-observed upcoming item becomes available.
func (s *Service) ObserveCalendar(profileID string, items []models.CalendarItem) {
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	today := time.Now().UTC().Format("2006-01-02")
	observations, err := s.repo.ListObservations(ctx, profileID)
	if err != nil {
		log.Printf("[notifications] list release observations profile=%s: %v", profileID, err)
		return
	}
	observationsByKey := make(map[string]models.NotificationObservation, len(observations))
	for _, observation := range observations {
		observationsByKey[observation.ItemKey] = observation
	}
	for _, item := range items {
		if item.Source != "watchlist" && item.Source != "top-trending" && item.Source != "trending" {
			continue
		}
		key := releaseObservationKey(item)
		previous, hadPrevious := observationsByKey[key]
		event := models.NotificationEvent{
			ID:            uuid.NewString(),
			Type:          models.NotificationEventRelease,
			ProfileID:     profileID,
			Title:         item.Title,
			MediaType:     item.MediaType,
			Year:          item.Year,
			EpisodeTitle:  item.EpisodeTitle,
			SeasonNumber:  item.SeasonNumber,
			EpisodeNumber: item.EpisodeNumber,
			ReleaseType:   item.ReleaseType,
			ReleaseDate:   item.AirDate,
			Source:        item.Source,
			SourceRank:    item.SourceRank,
			PosterURL:     firstNonEmpty(item.TextPosterURL, item.PosterURL),
			ExternalIDs:   item.ExternalIDs,
			OccurredAt:    time.Now().UTC(),
		}
		status := "upcoming"
		if item.AirDate != "" && item.AirDate <= today {
			status = "released"
		}
		observation := &models.NotificationObservation{
			ProfileID: profileID,
			ItemKey:   key,
			Status:    status,
			Event:     event,
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.repo.UpsertObservation(ctx, observation); err != nil {
			log.Printf("[notifications] save release observation profile=%s: %v", profileID, err)
			continue
		}
		observationsByKey[key] = *observation
		if hadPrevious && previous.Status != status && status == "released" {
			s.Notify(event)
		}
	}

	// Calendar builders intentionally drop releases after they become
	// available. Durable observations let us detect that transition even when
	// the released item is no longer present in the newly built calendar.
	for key, current := range observationsByKey {
		observation := current
		if observation.Status != "upcoming" || observation.Event.ReleaseDate == "" ||
			observation.Event.ReleaseDate > today {
			continue
		}
		observation.Status = "released"
		observation.UpdatedAt = time.Now().UTC()
		observation.Event.OccurredAt = observation.UpdatedAt
		if err := s.repo.UpsertObservation(ctx, &observation); err != nil {
			log.Printf("[notifications] release transition profile=%s: %v", profileID, err)
			continue
		}
		observationsByKey[key] = observation
		s.Notify(observation.Event)
	}
}

func (s *Service) Notify(event models.NotificationEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	channels, err := s.repo.ListChannels(ctx, event.ProfileID)
	cancel()
	if err != nil {
		log.Printf("[notifications] list channels profile=%s: %v", event.ProfileID, err)
		return
	}
	for _, channel := range channels {
		if !channel.Enabled || !contains(channel.Events, event.Type) || !channelAcceptsRelease(channel, event) {
			continue
		}
		if !s.claimDelivery(channel.ID, event) {
			continue
		}
		select {
		case s.deliveries <- delivery{channel: channel, event: event}:
		default:
			log.Printf("[notifications] delivery queue full; dropping event=%s profile=%s", event.Type, event.ProfileID)
		}
	}
}

func (s *Service) claimDelivery(channelID string, event models.NotificationEvent) bool {
	if event.Type != models.NotificationEventRelease {
		return true
	}
	identities := releaseEventIdentities(event)
	now := time.Now().UTC()
	s.deliveredMu.Lock()
	defer s.deliveredMu.Unlock()
	var matchedAt time.Time
	duplicate := false
	for _, identity := range identities {
		key := channelID + "\x00" + identity
		if deliveredAt, ok := s.delivered[key]; ok && now.Sub(deliveredAt) < 24*time.Hour {
			matchedAt = deliveredAt
			duplicate = true
			break
		}
	}
	if matchedAt.IsZero() {
		matchedAt = now
	}
	for _, identity := range identities {
		s.delivered[channelID+"\x00"+identity] = matchedAt
	}
	if duplicate {
		return false
	}
	if len(s.delivered) > 1024 {
		for existingKey, deliveredAt := range s.delivered {
			if now.Sub(deliveredAt) >= 24*time.Hour {
				delete(s.delivered, existingKey)
			}
		}
	}
	return true
}

// ReleaseRequirements tells the calendar worker which notification-only
// sources must be observed independently of the profile's calendar UI choices.
func (s *Service) ReleaseRequirements(profileID string) calendar.ReleaseRequirements {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channels, err := s.repo.ListChannels(ctx, profileID)
	if err != nil {
		log.Printf("[notifications] load release requirements profile=%s: %v", profileID, err)
		return calendar.ReleaseRequirements{}
	}
	var requirements calendar.ReleaseRequirements
	for _, channel := range channels {
		if !channel.Enabled || !contains(channel.Events, models.NotificationEventRelease) {
			continue
		}
		if channel.NotifyWatchlist {
			requirements.Watchlist = true
		}
		if channel.NotifyTrending && channel.TrendingLimit > requirements.TrendingLimit {
			requirements.TrendingLimit = channel.TrendingLimit
		}
	}
	return requirements
}

func (s *Service) run() {
	for {
		select {
		case item := <-s.deliveries:
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			var err error
			for attempt := 0; attempt < 3; attempt++ {
				err = s.deliver(ctx, item.channel, item.event)
				if err == nil {
					break
				}
				if attempt < 2 {
					select {
					case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
					case <-s.stop:
						cancel()
						return
					}
				}
			}
			if err != nil {
				log.Printf("[notifications] delivery failed channel=%s type=%s event=%s: %v",
					item.channel.ID, item.channel.Type, item.event.Type, err)
			}
			cancel()
		case <-s.stop:
			return
		}
	}
}

func (s *Service) deliver(ctx context.Context, channel models.NotificationChannel, event models.NotificationEvent) error {
	title, body := Format(channel, event)
	var payload any
	if channel.Type == models.NotificationChannelDiscord {
		title = truncate(title, 256)
		body = truncate(body, 4096)
		embed := map[string]any{
			"title":       title,
			"description": body,
			"timestamp":   event.OccurredAt.Format(time.RFC3339),
		}
		if channel.IncludePoster {
			if posterURL := publicHTTPURL(event.PosterURL); posterURL != "" {
				embed["thumbnail"] = map[string]string{"url": posterURL}
			}
		}
		payload = map[string]any{"embeds": []any{embed}}
	} else {
		payload = map[string]any{
			"event":   event.Type,
			"title":   title,
			"message": body,
			"data":    event,
		}
		if !channel.IncludePoster {
			payload.(map[string]any)["data"] = withoutPoster(event)
		}
	}
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.URL, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mediastorm-notifications/1.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// Format renders the two safe, non-executable notification template sections.
func Format(channel models.NotificationChannel, event models.NotificationEvent) (string, string) {
	values := templateValues(event)
	return render(channel.TitleTemplate, values), render(channel.BodyTemplate, values)
}

func templateValues(event models.NotificationEvent) map[string]string {
	title := event.Title
	if event.MediaType == "episode" && event.SeriesTitle != "" {
		title = event.SeriesTitle
	}
	episode := ""
	if event.SeasonNumber > 0 || event.EpisodeNumber > 0 {
		episode = fmt.Sprintf("S%02dE%02d", event.SeasonNumber, event.EpisodeNumber)
		if event.EpisodeTitle != "" {
			episode += " · " + event.EpisodeTitle
		}
	}
	mediaLabel := event.MediaType
	if mediaLabel != "" {
		mediaLabel = strings.ToUpper(mediaLabel[:1]) + mediaLabel[1:]
	}
	if episode != "" {
		mediaLabel += " · " + episode
	}
	progressLabel := ""
	if event.Percent > 0 {
		progressLabel = fmt.Sprintf(" · %.0f%%", event.Percent)
	}
	releaseLabel := ""
	if event.ReleaseType != "" || event.ReleaseDate != "" {
		releaseLabel = " · " + strings.TrimSpace(strings.Join(nonEmpty(event.ReleaseType, event.ReleaseDate), " · "))
	}
	return map[string]string{
		"event":         event.Type,
		"eventLabel":    eventLabel(event.Type),
		"title":         title,
		"year":          optionalInt(event.Year),
		"mediaType":     event.MediaType,
		"mediaLabel":    mediaLabel,
		"seriesTitle":   event.SeriesTitle,
		"episodeTitle":  event.EpisodeTitle,
		"episode":       episode,
		"season":        optionalInt(event.SeasonNumber),
		"episodeNumber": optionalInt(event.EpisodeNumber),
		"percent":       strconv.FormatFloat(event.Percent, 'f', 0, 64),
		"progressLabel": progressLabel,
		"releaseType":   event.ReleaseType,
		"releaseDate":   event.ReleaseDate,
		"releaseLabel":  releaseLabel,
		"source":        event.Source,
		"rank":          optionalInt(event.SourceRank),
		"posterUrl":     event.PosterURL,
	}
}

func render(template string, values map[string]string) string {
	result := template
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = strings.ReplaceAll(result, "{{"+key+"}}", values[key])
	}
	return strings.TrimSpace(result)
}

func validateDestination(kind, rawURL string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("a valid HTTP or HTTPS webhook URL is required")
	}
	if parsed.User != nil {
		return errors.New("webhook URLs cannot contain user information")
	}
	if kind == models.NotificationChannelDiscord {
		host := strings.ToLower(parsed.Hostname())
		if host != "discord.com" && host != "discordapp.com" {
			return errors.New("Discord webhooks must use discord.com")
		}
		if !strings.HasPrefix(parsed.Path, "/api/webhooks/") {
			return errors.New("invalid Discord webhook URL")
		}
	}
	return nil
}

func normalizeEvents(events []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(events))
	for _, event := range events {
		event = strings.TrimSpace(event)
		if validEvents[event] && !seen[event] {
			seen[event] = true
			result = append(result, event)
		}
	}
	sort.Strings(result)
	return result
}

func channelAcceptsRelease(channel models.NotificationChannel, event models.NotificationEvent) bool {
	if event.Type != models.NotificationEventRelease {
		return true
	}
	if event.Source == "watchlist" {
		return channel.NotifyWatchlist
	}
	if event.Source == "top-trending" || event.Source == "trending" {
		return channel.NotifyTrending && event.SourceRank > 0 && event.SourceRank <= channel.TrendingLimit
	}
	return false
}

func releaseObservationKey(item models.CalendarItem) string {
	source := item.Source
	if source == "top-trending" || source == "trending" {
		source = "trending"
	}
	return strings.Join([]string{source, item.MediaType, fallbackMediaIdentity(item.Title, item.Year),
		item.ReleaseType, item.AirDate,
		strconv.Itoa(item.SeasonNumber), strconv.Itoa(item.EpisodeNumber)}, "|")
}

func releaseEventIdentity(event models.NotificationEvent) string {
	return releaseEventIdentities(event)[0]
}

func releaseEventIdentities(event models.NotificationEvent) []string {
	identities := mediaIdentityAliases(event.ExternalIDs, event.Title, event.Year)
	result := make([]string, 0, len(identities))
	for _, identity := range identities {
		result = append(result, strings.Join([]string{
			event.MediaType, identity, event.ReleaseType, event.ReleaseDate,
			strconv.Itoa(event.SeasonNumber), strconv.Itoa(event.EpisodeNumber),
		}, "|"))
	}
	return result
}

func mediaIdentityAliases(externalIDs map[string]string, title string, year int) []string {
	normalized := make(map[string]string, len(externalIDs))
	for key, value := range externalIDs {
		key = strings.ToLower(strings.TrimSpace(key))
		key = strings.TrimSuffix(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""), "id")
		if value = strings.TrimSpace(value); value != "" {
			normalized[key] = strings.ToLower(value)
		}
	}
	identities := make([]string, 0, 4)
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if value := normalized[provider]; value != "" {
			identities = append(identities, provider+":"+value)
		}
	}
	return append(identities, fallbackMediaIdentity(title, year))
}

func fallbackMediaIdentity(title string, year int) string {
	return "title:" + strings.ToLower(strings.Join(strings.Fields(title), " ")) + ":" + strconv.Itoa(year)
}

func playbackTitle(update models.PlaybackProgressUpdate) string {
	if update.MediaType == "episode" {
		return firstNonEmpty(update.SeriesName, update.EpisodeName, update.ItemID)
	}
	return firstNonEmpty(update.MovieName, update.ItemID)
}

func eventLabel(event string) string {
	switch event {
	case models.NotificationEventWatchStarted:
		return "Started watching"
	case models.NotificationEventWatchPlaying:
		return "Now playing"
	case models.NotificationEventWatchResumed:
		return "Resumed"
	case models.NotificationEventWatchWatched:
		return "Watched"
	case models.NotificationEventRelease:
		return "Now available"
	default:
		return event
	}
}

func withoutPoster(event models.NotificationEvent) models.NotificationEvent {
	event.PosterURL = ""
	return event
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func optionalInt(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func publicHTTPURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
