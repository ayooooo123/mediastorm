package config

import "testing"

func TestSportsExplicitEmptyLeaguesRemainEmpty(t *testing.T) {
	s := SportsSettings{EnabledLeagues: []string{}}
	s.Normalize()
	if len(s.EnabledLeagues) != 0 {
		t.Fatal("empty leagues restored defaults")
	}
}

func TestSportsPollingSelection(t *testing.T) {
	off := false
	on := true
	for _, tc := range []struct {
		name     string
		settings SportsSettings
		want     int
	}{
		{"legacy", SportsSettings{}, len(defaultEnabledLeagueIDs)},
		{"disabled", SportsSettings{Enabled: &off, EnabledLeagues: []string{"*"}}, 0},
		{"empty", SportsSettings{Enabled: &on, EnabledLeagues: []string{}}, 0},
		{"selected", SportsSettings{Enabled: &on, EnabledLeagues: []string{"nba"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(tc.settings.PollingLeagueIDs()); got != tc.want {
				t.Fatalf("got %d leagues, want %d", got, tc.want)
			}
		})
	}
}
