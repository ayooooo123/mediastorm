package trackselect

import (
	"testing"

	"novastream/models"
)

func frenchDubs() []models.AudioStreamInfo {
	return []models.AudioStreamInfo{
		{Index: 1, Codec: "eac3", Language: "eng", Title: "VO"},
		{Index: 2, Codec: "eac3", Language: "fre", Title: "VFF"},
		{Index: 3, Codec: "eac3", Language: "fre", Title: "VFQ"},
	}
}

// The layout of Spider-Man.Brand.New.Day.2026.MULTi.VF2.2160p.WEB.H265-SUPPLY.
func spiderManDubs() []models.AudioStreamInfo {
	return []models.AudioStreamInfo{
		{Index: 1, Codec: "eac3", Language: "fre", Title: "French (France)"},
		{Index: 2, Codec: "aac", Language: "fre", Title: "French (France) AD"},
		{Index: 3, Codec: "eac3", Language: "fre", Title: "French (Canadien)"},
		{Index: 4, Codec: "eac3", Language: "eng", Title: "English"},
	}
}

func TestFindAudioTrackRegionalVariant(t *testing.T) {
	cases := []struct {
		name       string
		streams    []models.AudioStreamInfo
		preference string
		want       int
	}{
		{"plain French keeps the first French track", frenchDubs(), "fre", 2},
		{"fr-CA picks VFQ", frenchDubs(), "fr-CA", 3},
		{"fre-ca picks VFQ", frenchDubs(), "fre-ca", 3},
		{"fr-FR picks VFF", frenchDubs(), "fr-FR", 2},
		{"fr-CA falls back to any French", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "eng"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "VFF"},
		}, "fr-CA", 2},
		{"accented Québec label", []models.AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "fre", Title: "Français (France)"},
			{Index: 2, Codec: "ac3", Language: "fre", Title: "Français (Québec)"},
		}, "fr-CA", 2},
		{"region-tagged container language", []models.AudioStreamInfo{
			{Index: 1, Codec: "aac", Language: "fr"},
			{Index: 2, Codec: "aac", Language: "fr-CA"},
		}, "fr-CA", 2},
		{"regional codec order still prefers E-AC-3", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "VFF"},
			{Index: 2, Codec: "truehd", Language: "fre", Title: "VFQ TrueHD"},
			{Index: 3, Codec: "eac3", Language: "fre", Title: "VFQ DDP"},
		}, "fr-CA", 3},
		{"regional commentary loses to a French dub", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "VFF"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "VFQ Commentary"},
		}, "fr-CA", 1},
		{"es-419 picks Latino", []models.AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "spa", Title: "Castellano"},
			{Index: 2, Codec: "ac3", Language: "spa", Title: "Latino"},
		}, "es-419", 2},
		{"es-ES picks Castilian", []models.AudioStreamInfo{
			{Index: 1, Codec: "ac3", Language: "spa", Title: "Latino"},
			{Index: 2, Codec: "ac3", Language: "spa", Title: "Castellano"},
		}, "es-ES", 2},
		{"pt-BR picks Brazilian", []models.AudioStreamInfo{
			{Index: 1, Codec: "aac", Language: "por", Title: "Portugal"},
			{Index: 2, Codec: "aac", Language: "por", Title: "Brazilian"},
		}, "pt-BR", 2},
		{"French (Canadien) label picks the Canadian dub", spiderManDubs(), "fr-CA", 3},
		{"French (France) label for fr-FR", spiderManDubs(), "fr-FR", 1},
		{"audio description loses to the main French mix", []models.AudioStreamInfo{
			{Index: 1, Codec: "truehd", Language: "fre", Title: "French (France)"},
			{Index: 2, Codec: "aac", Language: "fre", Title: "French (France) AD"},
		}, "fre", 1},
		{"VQ label", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "VFF"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "VQ"},
		}, "fr-CA", 2},
		{"FR-CA in the title", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "FR-FR 5.1"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "FR-CA 5.1"},
		}, "fr-CA", 2},
		{"French CA in the title", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "French"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "French CA"},
		}, "fr-CA", 2},
		{"CA label on a French-tagged track", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "Stereo"},
			{Index: 2, Codec: "eac3", Language: "fre", Title: "CA 2.0"},
		}, "fr-CA", 2},
		{"CA label without a French tag is not a region", []models.AudioStreamInfo{
			{Index: 1, Codec: "eac3", Language: "fre", Title: "Stereo"},
			{Index: 2, Codec: "eac3", Language: "", Title: "Stereo CA"},
		}, "fr-CA", 1},
		{"unknown region behaves as the language", frenchDubs(), "fr-XX", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindAudioTrack(tc.streams, tc.preference); got != tc.want {
				t.Fatalf("FindAudioTrack(%q) = %d, want %d", tc.preference, got, tc.want)
			}
		})
	}
}

func TestSelectRegionalVariantWithAllowedLanguages(t *testing.T) {
	selection := Select(frenchDubs(), nil, Preferences{AudioLanguage: "fr-CA", AllowedLanguages: []string{"eng", "fra"}})
	if selection.Audio == nil || selection.Audio.StreamIndex != 3 {
		t.Fatalf("audio = %+v, want stream 3 (VFQ)", selection.Audio)
	}
}
