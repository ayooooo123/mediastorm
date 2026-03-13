package debrid

import (
	"sync"
	"testing"
)

func TestExtractInfoHashFromMagnet(t *testing.T) {
	tests := []struct {
		name     string
		magnet   string
		expected string
	}{
		{
			name:     "standard magnet with single hash",
			magnet:   "magnet:?xt=urn:btih:ABCDEF1234567890&dn=Example",
			expected: "abcdef1234567890",
		},
		{
			name:     "magnet with uppercase hash",
			magnet:   "magnet:?xt=urn:btih:FEDCBA0987654321&tr=http://tracker.example.com",
			expected: "fedcba0987654321",
		},
		{
			name:     "magnet without additional parameters",
			magnet:   "magnet:?xt=urn:btih:1234567890ABCDEF",
			expected: "1234567890abcdef",
		},
		{
			name:     "invalid magnet without btih",
			magnet:   "magnet:?xt=urn:sha1:ABCDEF",
			expected: "",
		},
		{
			name:     "empty string",
			magnet:   "",
			expected: "",
		},
		{
			name:     "magnet with spaces in hash (trimmed)",
			magnet:   "magnet:?xt=urn:btih:  ABC123  &dn=test",
			expected: "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractInfoHashFromMagnet(tt.magnet)
			if result != tt.expected {
				t.Errorf("extractInfoHashFromMagnet(%q) = %q, want %q", tt.magnet, result, tt.expected)
			}
		})
	}
}

func TestActiveTorrentTracking(t *testing.T) {
	hs := NewHealthService(nil)

	// Initially not active
	if hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should not be active initially")
	}

	// Mark active
	hs.MarkTorrentActive("torbox", "123")
	if !hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should be active after MarkTorrentActive")
	}

	// Different provider should not be active
	if hs.isTorrentActive("realdebrid", "123") {
		t.Fatal("different provider should not be active")
	}

	// Mark inactive
	hs.MarkTorrentInactive("torbox", "123")
	if hs.isTorrentActive("torbox", "123") {
		t.Fatal("torrent should not be active after MarkTorrentInactive")
	}
}

func TestActiveTorrentConcurrency(t *testing.T) {
	hs := NewHealthService(nil)

	var wg sync.WaitGroup
	// Simulate concurrent mark/check from health + playback goroutines
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			hs.MarkTorrentActive("torbox", "456")
			hs.isTorrentActive("torbox", "456")
		}()
		go func() {
			defer wg.Done()
			hs.isTorrentActive("torbox", "456")
			hs.MarkTorrentInactive("torbox", "456")
		}()
	}
	wg.Wait()
}
