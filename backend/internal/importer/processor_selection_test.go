package importer

import "testing"

func TestIsContentVideoPath(t *testing.T) {
	proc := &Processor{}

	tests := []struct {
		name         string
		internalPath string
		want         bool
	}{
		{
			name:         "main episode",
			internalPath: "Release/Show.S01E08.1080p.WEB.mkv",
			want:         true,
		},
		{
			name:         "sample filename before main",
			internalPath: "Release/Sample/Show.S01E08.sample.mkv",
			want:         false,
		},
		{
			name:         "sample directory with neutral filename",
			internalPath: "Release/Samples/clip.mkv",
			want:         false,
		},
		{
			name:         "extras filename",
			internalPath: "Release/Show.S01E08.extras.mp4",
			want:         false,
		},
		{
			name:         "trailer directory with neutral filename",
			internalPath: `Release\Trailers\clip.mkv`,
			want:         false,
		},
		{
			name:         "non-video",
			internalPath: "Release/Show.S01E08.srt",
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proc.isContentVideoPath(tt.internalPath); got != tt.want {
				t.Fatalf("isContentVideoPath(%q) = %t, want %t", tt.internalPath, got, tt.want)
			}
		})
	}
}

func TestArchiveEarlyPlaybackSkipsSampleBeforeMain(t *testing.T) {
	proc := &Processor{}
	archiveEntries := []string{
		"Release/Sample/Show.S01E08.sample.mkv",
		"Release/Show.S01E08.1080p.WEB.mkv",
	}

	var selected string
	for _, entry := range archiveEntries {
		if selected == "" && proc.isContentVideoPath(entry) {
			selected = entry
		}
	}

	if want := archiveEntries[1]; selected != want {
		t.Fatalf("selected %q, want main content %q", selected, want)
	}
}
