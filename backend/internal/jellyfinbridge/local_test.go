package jellyfinbridge

import "testing"

func TestLocalProgressIdentity(t *testing.T) {
	tests := []struct {
		name          string
		ref           itemRef
		wantType      string
		wantID        string
		wantTrackable bool
	}{
		{
			name:          "matched local movie",
			ref:           itemRef{Kind: refLocal, Ctx: refContext{TitleID: "tmdb:movie:10378"}},
			wantType:      "movie",
			wantID:        "tmdb:movie:10378",
			wantTrackable: true,
		},
		{
			// Plex/Jellyfin library items keep a localmedia: title ID but carry
			// the provider's catalog IDs.
			name:          "remote library movie with external IDs",
			ref:           itemRef{Kind: refLocal, Ctx: refContext{TitleID: "localmedia:6caa11ca", ExternalIDs: map[string]string{"tmdb": "10378", "imdb": "tt1254207"}}},
			wantType:      "movie",
			wantID:        "tmdb:movie:10378",
			wantTrackable: true,
		},
		{
			name:          "remote library movie with only an IMDb ID",
			ref:           itemRef{Kind: refLocal, Ctx: refContext{TitleID: "localmedia:6caa11ca", ExternalIDs: map[string]string{"imdb": "tt1254207"}}},
			wantType:      "movie",
			wantID:        "tt1254207",
			wantTrackable: true,
		},
		{
			name: "unmatched local movie",
			ref:  itemRef{Kind: refLocal, Ctx: refContext{TitleID: "localmedia:6caa11ca"}},
		},
		{
			name:          "local episode with series IDs",
			ref:           itemRef{Kind: refLocal, Ctx: refContext{TitleID: "localmedia:abc", ViewKey: "group", ExternalIDs: map[string]string{"tmdb": "1399"}, Season: 1, Episode: 2}},
			wantType:      "episode",
			wantID:        "tmdb:tv:1399:s01e02",
			wantTrackable: true,
		},
		{
			name: "local episode without IDs",
			ref:  itemRef{Kind: refLocal, Ctx: refContext{TitleID: "localmedia:abc", ViewKey: "group", Season: 1, Episode: 2}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotID, ok := localProgressIdentity(tc.ref)
			if ok != tc.wantTrackable || gotType != tc.wantType || gotID != tc.wantID {
				t.Fatalf("localProgressIdentity = (%q, %q, %v), want (%q, %q, %v)", gotType, gotID, ok, tc.wantType, tc.wantID, tc.wantTrackable)
			}
		})
	}
}
