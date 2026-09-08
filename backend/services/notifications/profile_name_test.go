package notifications

import (
	"novastream/models"
	"testing"
)

func TestFormatProfileName(t *testing.T) {
	for _, tc := range []struct {
		name, kind, event, profile, template, want string
		enabled                                    bool
	}{
		{"default off", "discord", "watch.progress", "godver3", defaultTitleTemplate, "Watching: Movie", false},
		{"watching", "discord", "watch.progress", "godver3", defaultTitleTemplate, "godver3 - Watching: Movie", true},
		{"watched", "discord", "watch.watched", "godver3", defaultTitleTemplate, "godver3 - Watched: Movie", true},
		{"missing profile", "discord", "watch.watched", "  ", defaultTitleTemplate, "Watched: Movie", true},
		{"custom placement", "discord", "watch.watched", "godver3", "{{eventLabel}} by {{profileName}}: {{title}}", "Watched by godver3: Movie", true},
		{"explicit template with toggle off", "discord", "watch.watched", "godver3", "{{profileName}} - {{eventLabel}}", "godver3 - Watched", false},
		{"webhook", "webhook", "watch.watched", "godver3", defaultTitleTemplate, "Watched: Movie", true},
		{"release", "discord", "release.available", "godver3", "{{title}}", "Movie", true},
		{"system", "discord", "system.startup", "godver3", "{{title}}", "Movie", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := models.NotificationChannel{Type: tc.kind, IncludeProfileName: tc.enabled, TitleTemplate: tc.template, BodyTemplate: "{{profileName}}"}
			title, body := Format(channel, models.NotificationEvent{Type: tc.event, ProfileName: tc.profile, Title: "Movie"})
			if title != tc.want {
				t.Fatalf("title = %q, want %q", title, tc.want)
			}
			if tc.profile == "godver3" && body != tc.profile {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

type notificationProfiles map[string]models.User

func (p notificationProfiles) Get(id string) (models.User, bool) { u, ok := p[id]; return u, ok }

func TestNotificationProfileNameUsesDestinationOwner(t *testing.T) {
	profiles := notificationProfiles{"owner": {Name: "godver3"}}
	s := &Service{profiles: profiles}
	channel := models.NotificationChannel{ProfileID: "owner"}
	event := models.NotificationEvent{ProfileID: "another"}
	if got := s.withProfileName(channel, event).ProfileName; got != "godver3" {
		t.Fatalf("name = %q", got)
	}
	profiles["owner"] = models.User{Name: "Renamed"}
	if got := s.withProfileName(channel, event).ProfileName; got != "Renamed" {
		t.Fatalf("renamed = %q", got)
	}
	delete(profiles, "owner")
	if got := s.withProfileName(channel, event).ProfileName; got != "" {
		t.Fatalf("missing name = %q", got)
	}
}
