package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"novastream/services/streaming"
)

type relatedFileTestProvider struct {
	relatedPath string
	body        string
}

func (p *relatedFileTestProvider) Stream(context.Context, streaming.Request) (*streaming.Response, error) {
	return nil, streaming.ErrNotFound
}

func (p *relatedFileTestProvider) StreamRelatedFile(_ context.Context, _ string, relatedPath string) (*streaming.Response, error) {
	p.relatedPath = relatedPath
	return &streaming.Response{
		Body:          io.NopCloser(strings.NewReader(p.body)),
		ContentLength: int64(len(p.body)),
		Status:        http.StatusOK,
	}, nil
}

func TestBluRayCLPIPath(t *testing.T) {
	path, ok := bluRayCLPIPath("/debrid/torbox/56484571/file/180/Black.Swan.2010.Blu-ray/BDMV/STREAM/00801.m2ts")
	if !ok {
		t.Fatal("bluRayCLPIPath() did not recognize Blu-ray stream path")
	}
	if want := "Black.Swan.2010.Blu-ray/BDMV/CLIPINF/00801.clpi"; path != want {
		t.Fatalf("bluRayCLPIPath() = %q, want %q", path, want)
	}

	if _, ok := bluRayCLPIPath("/debrid/torbox/1/file/2/Movie.mkv"); ok {
		t.Fatal("bluRayCLPIPath() recognized non-Blu-ray path")
	}
}

func TestParseCLPIStreamLanguages(t *testing.T) {
	data := []byte{
		0x11, 0x00, 0x15, 0x86, 0x61, 'e', 'n', 'g',
		0, 0,
		0x12, 0x00, 0x15, 0x90, 'e', 'n', 'g',
		0, 0,
		0x12, 0x01, 0x15, 0x90, 'f', 'r', 'a',
	}

	languages := parseCLPIStreamLanguages(data)
	for pid, want := range map[int]string{0x1100: "eng", 0x1200: "eng", 0x1201: "fra"} {
		if got := languages[pid]; got != want {
			t.Errorf("language for PID %#x = %q, want %q", pid, got, want)
		}
	}
}

func TestEnrichBluRayStreamLanguages(t *testing.T) {
	clpi := string([]byte{
		0x12, 0x00, 0x15, 0x90, 'e', 'n', 'g',
		0, 0,
		0x12, 0x01, 0x15, 0x90, 'f', 'r', 'a',
	})
	provider := &relatedFileTestProvider{body: clpi}
	handler := NewVideoHandlerWithProvider(false, "", "", "", provider)
	meta := &ffprobeOutput{Streams: []ffprobeStream{
		{Index: 10, ID: "0x1200", CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle"},
		{Index: 11, ID: "0x1201", CodecType: "subtitle", CodecName: "hdmv_pgs_subtitle", Tags: map[string]string{"language": "deu"}},
	}}

	handler.enrichBluRayStreamLanguages(context.Background(), "/debrid/torbox/56484571/file/180/Disc/BDMV/STREAM/00801.m2ts", meta)

	if got := meta.Streams[0].Tags["language"]; got != "eng" {
		t.Fatalf("enriched language = %q, want eng", got)
	}
	if got := meta.Streams[1].Tags["language"]; got != "deu" {
		t.Fatalf("existing language overwritten: got %q, want deu", got)
	}
	if want := "Disc/BDMV/CLIPINF/00801.clpi"; provider.relatedPath != want {
		t.Fatalf("related path = %q, want %q", provider.relatedPath, want)
	}
}
