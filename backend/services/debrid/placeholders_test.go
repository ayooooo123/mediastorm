package debrid

import "testing"

func TestIsKnownPlaceholderURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "ElfHosted slate host",
			url:  "https://slate.elfhosted.com/cache/abc/playlist.m3u8",
			want: true,
		},
		{
			name: "known placeholder filename",
			url:  "https://example.com/assets/downloading.mp4",
			want: true,
		},
		{
			name: "normal Comet playback URL",
			url:  "https://comet.elfhosted.com/playback/abc",
			want: false,
		},
		{
			name: "normal debrid stream",
			url:  "https://store.example.com/movie.mkv",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsKnownPlaceholderURL(tt.url); got != tt.want {
				t.Fatalf("IsKnownPlaceholderURL(%q) = %t, want %t", tt.url, got, tt.want)
			}
		})
	}
}

func TestIsKnownPlaceholderResponse(t *testing.T) {
	playlist := []byte("#EXTM3U\n#EXTINF:120.960,\nhttps://slate.elfhosted.com/cache/abc/seg.ts\n")
	if !IsKnownPlaceholderResponse("https://comet.elfhosted.com/playback/abc", playlist) {
		t.Fatal("expected ElfHosted slate playlist to be recognized as a placeholder")
	}

	if IsKnownPlaceholderResponse("https://store.example.com/movie.mkv", []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		t.Fatal("expected Matroska bytes to remain playable")
	}
}
