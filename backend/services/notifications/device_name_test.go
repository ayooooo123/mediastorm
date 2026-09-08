package notifications

import (
	"errors"
	"testing"

	"novastream/models"
)

type notificationClients struct {
	client *models.Client
	err    error
}

func (p notificationClients) Get(string) (*models.Client, error) { return p.client, p.err }

func TestNotificationDeviceNameFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *models.Client
		err    error
		want   string
	}{
		{"nickname", &models.Client{Nickname: " Living Room TV ", Name: "Display", DeviceName: "OS", DeviceType: "TV"}, nil, "Living Room TV"},
		{"display", &models.Client{Nickname: " ", Name: "Display", DeviceName: "OS", DeviceType: "TV"}, nil, "Display"},
		{"OS", &models.Client{DeviceName: "OS", DeviceType: "TV"}, nil, "OS"},
		{"type", &models.Client{DeviceType: "TV"}, nil, "TV"},
		{"empty", &models.Client{}, nil, ""},
		{"unknown", nil, nil, ""},
		{"error", nil, errors.New("unavailable"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{clients: notificationClients{tc.client, tc.err}}
			if got := s.withDeviceName(models.NotificationEvent{ClientID: "device"}).DeviceName; got != tc.want {
				t.Fatalf("device name = %q, want %q", got, tc.want)
			}
			if got := s.withDeviceName(models.NotificationEvent{}).DeviceName; got != "" {
				t.Fatalf("missing ID resolved to %q", got)
			}
		})
	}
}

func TestFormatDeviceName(t *testing.T) {
	for _, tc := range []struct {
		name, template, kind, event, device, want string
		enabled, profile                          bool
	}{
		{"default off", defaultTitleTemplate, "discord", "watch.progress", "TV", "Watching: Movie", false, false},
		{"device only", defaultTitleTemplate, "discord", "watch.progress", "TV", "TV - Watching: Movie", true, false},
		{"both", defaultTitleTemplate, "discord", "watch.watched", "TV", "godver3 - TV - Watched: Movie", true, true},
		{"missing", defaultTitleTemplate, "discord", "watch.watched", "", "godver3 - Watched: Movie", true, true},
		{"custom", "{{profileName}} - {{eventLabel}} on {{deviceName}}", "discord", "watch.watched", "TV", "godver3 - Watched on TV", true, true},
		{"explicit off", "{{eventLabel}} on {{deviceName}}", "discord", "watch.watched", "TV", "Watched on TV", false, false},
		{"webhook", defaultTitleTemplate, "webhook", "watch.watched", "TV", "Watched: Movie", true, true},
		{"release", "{{title}}", "discord", "release.available", "TV", "Movie", true, true},
		{"system", "{{title}}", "discord", "system.startup", "TV", "Movie", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, body := Format(models.NotificationChannel{Type: tc.kind, IncludeDeviceName: tc.enabled, IncludeProfileName: tc.profile, TitleTemplate: tc.template, BodyTemplate: "{{deviceName}}"}, models.NotificationEvent{Type: tc.event, DeviceName: tc.device, ProfileName: "godver3", Title: "Movie"})
			if title != tc.want || body != tc.device {
				t.Fatalf("title/body = %q / %q, want %q / %q", title, body, tc.want, tc.device)
			}
		})
	}
}
