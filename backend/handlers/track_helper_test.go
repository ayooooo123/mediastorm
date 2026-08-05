package handlers

import "testing"

func TestFindAudioTrackByLanguagePrefersEAC3OverEarlierCompatibleFallbacks(t *testing.T) {
	streams := []AudioStreamInfo{
		{Index: 1, Codec: "aac", Language: "eng", Title: "English AAC"},
		{Index: 2, Codec: "ac3", Language: "eng", Title: "English 5.1"},
		{Index: 3, Codec: "eac3", Language: "eng", Title: "English DDP Atmos"},
	}

	if got := FindAudioTrackByLanguage(streams, "eng"); got != 3 {
		t.Fatalf("FindAudioTrackByLanguage() = %d, want E-AC-3 track 3", got)
	}
}

func TestFindAudioTrackByLanguageSkipsEAC3CommentaryForRegularCompatibleTrack(t *testing.T) {
	streams := []AudioStreamInfo{
		{Index: 1, Codec: "ac3", Language: "eng", Title: "English 5.1"},
		{Index: 2, Codec: "eac3", Language: "eng", Title: "Director Commentary"},
	}

	if got := FindAudioTrackByLanguage(streams, "eng"); got != 1 {
		t.Fatalf("FindAudioTrackByLanguage() = %d, want regular AC-3 track 1", got)
	}
}

func TestFindSubtitleTrackByPreferenceSkipsUnlabeledTracks(t *testing.T) {
	streams := []SubtitleStreamInfo{
		{Index: 0, Language: "", Title: "", IsForced: false},
		{Index: 1, Language: "eng", Title: "English", IsForced: false},
	}

	got := FindSubtitleTrackByPreference(streams, "eng", "on", "eng")
	if got != 1 {
		t.Fatalf("expected English subtitle track 1, got %d", got)
	}
}

func TestFindSubtitleTrackByPreferenceForcedOnlySkipsUnlabeledTracks(t *testing.T) {
	streams := []SubtitleStreamInfo{
		{Index: 0, Language: "", Title: "Forced", IsForced: true},
		{Index: 1, Language: "eng", Title: "English Forced", IsForced: true},
	}

	got := FindSubtitleTrackByPreference(streams, "eng", "forced-only", "eng")
	if got != 1 {
		t.Fatalf("expected English forced subtitle track 1, got %d", got)
	}
}

func TestFindSubtitleTrackByPreferenceSkipsDVDSubtitles(t *testing.T) {
	streams := []SubtitleStreamInfo{
		{Index: 0, Codec: "dvd_subtitle", Language: "eng", Title: "English DVD", IsForced: false},
		{Index: 1, Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English PGS", IsForced: false},
	}

	got := FindSubtitleTrackByPreference(streams, "eng", "on", "eng")
	if got != 1 {
		t.Fatalf("expected PGS subtitle track 1, got %d", got)
	}
}
