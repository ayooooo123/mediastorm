package playback

import (
	"strings"
	"testing"

	"novastream/config"
	metapb "novastream/internal/nzb/metadata/proto"
	"novastream/models"
)

type validationMetadataService struct {
	fileSizes map[string]int64
	files     map[string][]string
}

func (s validationMetadataService) ListDirectory(virtualPath string) ([]string, error) {
	return s.files[virtualPath], nil
}

func (s validationMetadataService) ListSubdirectories(string) ([]string, error) {
	return nil, nil
}

func (s validationMetadataService) GetFileMetadata(virtualPath string) (*metapb.FileMetadata, error) {
	size, ok := s.fileSizes[virtualPath]
	if !ok {
		return nil, nil
	}
	return &metapb.FileMetadata{FileSize: size}, nil
}

func TestExpectedPerFileSizePolicy(t *testing.T) {
	tests := []struct {
		name      string
		candidate models.NZBResult
		want      int64
	}{
		{
			name: "episode specific release uses advertised size",
			candidate: models.NZBResult{
				Title:     "Show.S02E01.1080p.WEB-DL",
				SizeBytes: 1800,
			},
			want: 1800,
		},
		{
			name: "target season attributes do not override title without SxxExx",
			candidate: models.NZBResult{
				Title:     "Show.2x1.1080p.WEB-DL",
				SizeBytes: 1800,
				Attributes: map[string]string{
					"targetSeason":  "2",
					"targetEpisode": "1",
				},
			},
			want: 0,
		},
		{
			name: "target episode code identifies episode release",
			candidate: models.NZBResult{
				Title:     "Show.S02E01.1080p.WEB-DL",
				SizeBytes: 1800,
				Attributes: map[string]string{
					"targetEpisodeCode": "S02E01",
				},
			},
			want: 1800,
		},
		{
			name: "season pack total is not treated as per file",
			candidate: models.NZBResult{
				Title:     "Show.S02.1080p.WEB-DL",
				SizeBytes: 9000,
			},
			want: 0,
		},
		{
			name: "multi episode result is not treated as per file",
			candidate: models.NZBResult{
				Title:        "Show.S02E01-E10.1080p.WEB-DL",
				SizeBytes:    9000,
				EpisodeCount: 10,
			},
			want: 0,
		},
		{
			name: "size per file overrides pack-looking title",
			candidate: models.NZBResult{
				Title:       "Show.S02.1080p.WEB-DL",
				SizeBytes:   900,
				SizePerFile: true,
			},
			want: 900,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expectedPerFileSize(tt.candidate); got != tt.want {
				t.Fatalf("expectedPerFileSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidateResolvedMediaFileSizePolicy(t *testing.T) {
	const selectedPath = "/virtual/Show.S02.1080p/Show.S02E01.Tiny.mkv"
	service := &Service{
		metadataSvc: validationMetadataService{
			fileSizes: map[string]int64{
				selectedPath: 80 * 1024 * 1024,
			},
		},
	}

	tests := []struct {
		name      string
		candidate models.NZBResult
		wantErr   bool
	}{
		{
			name: "size per file mismatch rejects pack-looking result",
			candidate: models.NZBResult{
				Title:       "Show.S02.1080p.WEB-DL",
				SizeBytes:   900 * 1024 * 1024,
				SizePerFile: true,
			},
			wantErr: true,
		},
		{
			name: "season pack total does not reject small selected file",
			candidate: models.NZBResult{
				Title:     "Show.S02.1080p.WEB-DL",
				SizeBytes: 9000 * 1024 * 1024,
			},
		},
		{
			name: "episode count prevents pack total comparison",
			candidate: models.NZBResult{
				Title:        "Show.S02E01-E10.1080p.WEB-DL",
				SizeBytes:    9000 * 1024 * 1024,
				EpisodeCount: 10,
			},
		},
		{
			name: "episode-specific mismatch rejects",
			candidate: models.NZBResult{
				Title:     "Show.S02E01.1080p.WEB-DL",
				SizeBytes: 900 * 1024 * 1024,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.validateResolvedMediaFile(selectedPath, "/virtual/Show.S02.1080p", tt.candidate)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected suspicious size error, got nil")
				}
				if !strings.Contains(err.Error(), "short sample") {
					t.Fatalf("expected short sample error, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateResolvedMediaFile returned error: %v", err)
			}
		})
	}
}

func TestResolvedFileConflictsWithTargetEpisode(t *testing.T) {
	candidate := models.NZBResult{Attributes: map[string]string{
		"targetSeason":  "1",
		"targetEpisode": "1",
	}}

	if !resolvedFileConflictsWithTargetEpisode("/release/Show.S01E11.mkv", candidate) {
		t.Fatal("expected S01E11 to conflict with requested S01E01")
	}
	if resolvedFileConflictsWithTargetEpisode("/release/Show.S01E01.mkv", candidate) {
		t.Fatal("did not expect S01E01 to conflict with requested S01E01")
	}
}

func TestBuildInternalPlaybackResolutionReselectsCachedSeasonPackEpisode(t *testing.T) {
	const releaseDir = "/release/Jackie.Chan.Adventures.S01"
	service := &Service{metadataSvc: validationMetadataService{
		files: map[string][]string{
			releaseDir: {
				"Jackie.Chan.Adventures.S01E11.mkv",
				"Jackie.Chan.Adventures.S01E01.mkv",
			},
		},
	}}
	candidate := models.NZBResult{
		Title: "Jackie.Chan.Adventures.S01.1080p.WEB-DL",
		Attributes: map[string]string{
			"targetSeason":  "1",
			"targetEpisode": "1",
		},
	}

	resolution, err := service.buildInternalPlaybackResolution(
		config.Settings{WebDAV: config.WebDAVSettings{Prefix: "/webdav"}},
		candidate,
		releaseDir+"/Jackie.Chan.Adventures.S01E11.mkv",
		"Jackie.Chan.Adventures.S01.nzb",
		0,
		"healthy",
	)
	if err != nil {
		t.Fatalf("buildInternalPlaybackResolution returned error: %v", err)
	}
	if !strings.HasSuffix(resolution.WebDAVPath, "/Jackie.Chan.Adventures.S01E01.mkv") {
		t.Fatalf("selected %q, want S01E01", resolution.WebDAVPath)
	}
}
