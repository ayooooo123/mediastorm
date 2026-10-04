package filter

import (
	"strings"
	"testing"
)

func TestRealDebridFilenameRules(t *testing.T) {
	if err := ValidateTerms([]string{RealDebridRestrictedReleaseTerm + "=10"}); err != nil {
		t.Fatal(err)
	}
	terms := CompileTerms([]string{RealDebridRestrictedReleaseTerm + "=10"})
	tests := []struct {
		name    string
		blocked bool
	}{
		{"WEB-DL", true},
		{"WEB.x264", true},
		{"WEB.H264", true},
		{"HDTV.x264", true},
		{"HDTV.XviD", true},
		{"WEB-DLRip", true},
		{"prefixWEB.H264suffix", true},
		{"web-dl", false},
		{"Web-Dl", false},
		{"WEB-dl", false},
		{"WEB.DL", false},
		{"WEBDL", false},
		{"WEB DL", false},
		{"WEB_DL", false},
		{"WEB.h264", false},
		{"web.H264", false},
		{"wEB.H264", false},
		{"web.x264", false},
		{"WEB.X264", false},
		{"hdtv.x264", false},
		{"HDTV.X264", false},
		{"HDTV.XVID", false},
		{"HDTV.xvid", false},
		{"WEB-x264", false},
		{"WEB H264", false},
		{"WEB_H264", false},
		{"HDTV x264", false},
		{"WEB.H.264", false},
		{"HDTV.H.264", false},
		{"HDTV.h264", false},
		{"WEB.Line.H264", false},
		{"WEB.x265", false},
		{"WEB.XviD", false},
		{"WEB.AVC", false},
		{"HDTV.x265", false},
		{"BluRay.x264", false},
		{"BluRay.DTS", false},
		{"WEBRip.x264", false},
		{"BDRip.x264", false},
		{"HDRip.XviD", false},
		{"DVDRip.XviD", false},
		{"PreDVDRip", false},
		{"2160p.UHD.BluRay.REMUX.DV.TrueHD.Atmos", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, title := range []string{tt.name, "Movie.2026.1080p." + tt.name + "-GROUP.mkv", "[GRP] Movie [" + tt.name + " 1080p].txt"} {
				if got := MatchesAnyTerm(title, terms); got != tt.blocked {
					t.Errorf("MatchesAnyTerm(%q) = %t, want %t", title, got, tt.blocked)
				}
				weight, _ := SumMatchedWeights(title, terms)
				wantWeight := 0
				if tt.blocked {
					wantWeight = 10
				}
				if weight != wantWeight {
					t.Errorf("SumMatchedWeights(%q) = %d, want %d", title, weight, wantWeight)
				}
			}
		})
	}

	// The RD preset must not change normal user terms' case-insensitive behavior.
	if !MatchesAnyTerm("web-dl", CompileTerms([]string{"WEB-DL"})) ||
		!MatchesAnyTerm("web.h264", CompileTerms([]string{`/WEB\.H264/`})) {
		t.Fatal("ordinary user terms should remain case-insensitive")
	}
	for _, name := range []string{"WEB-DL", "WEB.x264", "WEB.H264", "HDTV.x264", "HDTV.XviD"} {
		if MatchesAnyTerm(strings.ToLower(name), terms) {
			t.Errorf("lowercase %q should pass", name)
		}
	}
}
