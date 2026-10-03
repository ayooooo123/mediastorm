package epg

import (
	"testing"
	"time"

	"novastream/models"
)

func TestNowPlayingMatchesHDHomeRunAliases(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{schedule: &models.EPGSchedule{
		Channels: map[string]models.EPGChannel{
			"station-id": {ID: "station-id", Name: "5.1", Aliases: []string{"5.1", "TESTTV", "testtv", "TESTTV HD", "", " "}},
		},
		Programs: map[string][]models.EPGProgram{
			"station-id": {
				{ChannelID: "station-id", Title: "Current", Start: now.Add(-time.Hour), Stop: now.Add(time.Hour)},
				{ChannelID: "station-id", Title: "Next", Start: now.Add(time.Hour), Stop: now.Add(2 * time.Hour)},
			},
		},
	}}
	service.rebuildScheduleIndexLocked()
	for _, id := range []string{"station-id", "5.1", "TESTTV", "testtv", "TESTTV HD"} {
		t.Run(id, func(t *testing.T) {
			result := service.GetNowPlaying([]string{id})
			if len(result) != 1 || result[0].Current == nil || result[0].Current.Title != "Current" || result[0].Next == nil || result[0].Next.Title != "Next" {
				t.Fatalf("callsign/number did not resolve current and next programmes: %+v", result)
			}
			if result[0].ChannelID != id {
				t.Fatalf("request ID was changed: %q", result[0].ChannelID)
			}
			programs := service.GetSchedule(id, now.Add(-time.Hour), now.Add(time.Hour))
			if len(programs) != 1 || programs[0].ChannelID != result[0].Current.ChannelID {
				t.Fatal("now-playing and schedule resolved different stations")
			}
		})
	}
}

func TestNowPlayingRejectsAmbiguousAliases(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{schedule: &models.EPGSchedule{
		Channels: map[string]models.EPGChannel{
			"one":   {ID: "one", Name: "5.1", Aliases: []string{"5.1", "SHARED", "PRIMARY"}},
			"two":   {ID: "two", Name: "PRIMARY", Aliases: []string{"7.1", "shared HD"}},
			"three": {ID: "three", Name: "9.1", Aliases: []string{"SHARED"}},
		},
		Programs: map[string][]models.EPGProgram{},
	}}
	for _, id := range []string{"one", "two", "three"} {
		service.schedule.Programs[id] = []models.EPGProgram{{ChannelID: id, Start: now.Add(-time.Hour), Stop: now.Add(time.Hour)}}
	}
	for _, id := range []string{"SHARED", "PRIMARY", "", "!!!", "missing"} {
		result := service.GetNowPlaying([]string{id})
		if len(result) != 1 || result[0].Current != nil || result[0].Next != nil {
			t.Errorf("ambiguous or empty alias %q unexpectedly resolved: %+v", id, result)
		}
	}
	for _, id := range []string{"one", "two", "three", "5.1", "7.1", "9.1"} {
		result := service.GetNowPlaying([]string{id})
		if len(result) != 1 || result[0].Current == nil {
			t.Errorf("unique identifier %q failed: %+v", id, result)
		}
	}
}
