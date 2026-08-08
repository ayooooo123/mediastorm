package scrob

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

// UserService provides profile-to-Scrob account associations.
type UserService interface {
	Get(id string) (models.User, bool)
}

// Scrobbler pushes newly completed local watch-history items to the Scrob
// account linked to the profile.
type Scrobbler struct {
	client        *Client
	configManager *config.Manager
	userService   UserService
}

func NewScrobbler(client *Client, configManager *config.Manager) *Scrobbler {
	return &Scrobbler{client: client, configManager: configManager}
}

func (s *Scrobbler) SetUserService(userService UserService) { s.userService = userService }

func (s *Scrobbler) IsEnabled() bool {
	if s.configManager == nil {
		return false
	}
	settings, err := s.configManager.Load()
	if err != nil {
		return false
	}
	for i := range settings.Scrob.Accounts {
		if scrobAccountCanPush(&settings.Scrob.Accounts[i]) {
			return true
		}
	}
	return false
}

func (s *Scrobbler) IsEnabledForUser(userID string) bool {
	return scrobAccountCanPush(s.getAccountForUser(userID))
}

func (s *Scrobbler) getAccountForUser(userID string) *config.ScrobAccount {
	if s.userService == nil || s.configManager == nil {
		return nil
	}
	user, ok := s.userService.Get(userID)
	if !ok || strings.TrimSpace(user.ScrobAccountID) == "" {
		return nil
	}
	settings, err := s.configManager.Load()
	if err != nil {
		return nil
	}
	return settings.Scrob.GetAccountByID(user.ScrobAccountID)
}

func scrobAccountCanPush(account *config.ScrobAccount) bool {
	return account != nil && strings.TrimSpace(account.BaseURL) != "" && strings.TrimSpace(account.APIKey) != "" &&
		strings.TrimSpace(account.Username) != "" && account.Password != ""
}

func (s *Scrobbler) ScrobbleMovie(userID string, tmdbID, _ int, _ string, watchedAt time.Time) error {
	if tmdbID <= 0 {
		return nil
	}
	return s.push(userID, WatchEvent{TMDBID: tmdbID, MediaType: "movie", WatchedAt: scrobbleTime(watchedAt), Completed: true})
}

func (s *Scrobbler) ScrobbleEpisode(userID string, showTVDBID, season, episode int, watchedAt time.Time, externalIDs map[string]string) error {
	event, ok := scrobEpisodeEvent(showTVDBID, season, episode, watchedAt, externalIDs)
	if !ok {
		log.Printf("[scrob] skipping episode push for user %s: missing show TMDB ID or episode coordinates", userID)
		return nil
	}
	return s.push(userID, event)
}

func scrobEpisodeEvent(showTVDBID, season, episode int, watchedAt time.Time, externalIDs map[string]string) (WatchEvent, bool) {
	showTMDBID := positiveInt(externalIDs["tmdb"])
	if showTMDBID == 0 || season < 0 || episode <= 0 {
		return WatchEvent{}, false
	}
	if showTVDBID == 0 {
		showTVDBID = positiveInt(externalIDs["tvdb"])
	}
	return WatchEvent{
		TMDBID: positiveInt(externalIDs["episodeTmdb"]), MediaType: "episode", WatchedAt: scrobbleTime(watchedAt), Completed: true,
		SeriesTMDBID: showTMDBID, SeriesTVDBID: showTVDBID, SeasonNumber: season, EpisodeNumber: episode,
	}, true
}

func (s *Scrobbler) push(userID string, event WatchEvent) error {
	account := s.getAccountForUser(userID)
	if !scrobAccountCanPush(account) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	token, err := s.login(ctx, account)
	if err != nil {
		return err
	}
	return s.client.AddHistory(ctx, account.BaseURL, account.APIKey, token, event)
}

func (s *Scrobbler) login(ctx context.Context, account *config.ScrobAccount) (string, error) {
	code := ""
	var err error
	if strings.TrimSpace(account.TOTPSecret) != "" {
		code, err = GenerateTOTPCode(account.TOTPSecret, time.Now().UTC())
		if err != nil {
			return "", err
		}
	}
	return s.client.Login(ctx, account.BaseURL, account.APIKey, account.Username, account.Password, code)
}

func scrobbleTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func positiveInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	if n > 0 {
		return n
	}
	return 0
}
