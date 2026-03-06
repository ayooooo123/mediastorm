package handlers

import "testing"

func TestNormalizeTranslationLanguageCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "iso 639-2 eng", in: "eng", want: "en"},
		{name: "iso 639-2 deu", in: "deu", want: "de"},
		{name: "legacy alias", in: "ger", want: "de"},
		{name: "language name", in: "German", want: "de"},
		{name: "emoji flag", in: "🇩🇪", want: "de"},
		{name: "bcp47 tag", in: "pt-BR", want: "pt"},
		{name: "auto", in: "auto", want: "auto"},
		{name: "invalid", in: "🚫", want: ""},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTranslationLanguageCode(tt.in)
			if got != tt.want {
				t.Fatalf("normalizeTranslationLanguageCode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
