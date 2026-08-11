package debrid

import (
	"strings"
	"testing"
)

func TestParseTorboxCheckCachedResponseHashKeyedObject(t *testing.T) {
	body := []byte(`{
		"success": true,
		"error": null,
		"detail": "Torrent cache status retrieved successfully.",
		"data": {
			"152758f0790431f569396c00cdc49f76546d82be": {
				"name": "Good.Luck.Have.Fun.Dont.Die.2025.2160p.UHD.BluRay.Remux.DV.P7.HDR.TrueHD.Atmos.H265-BEN.THE.MEN",
				"size": 86044281186,
				"hash": "152758f0790431f569396c00cdc49f76546d82be",
				"files": [
					{"name": "movie.mkv", "size": 86044280413}
				]
			}
		}
	}`)

	cached, detail, err := parseTorboxCheckCachedResponse(body)
	if err != nil {
		t.Fatalf("parseTorboxCheckCachedResponse() error = %v", err)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	if !cached["152758f0790431f569396c00cdc49f76546d82be"] {
		t.Fatal("expected hash-keyed Torbox cache response to mark hash cached")
	}
}

func TestParseTorboxCheckCachedResponseArray(t *testing.T) {
	body := []byte(`{
		"success": true,
		"error": null,
		"detail": "Torrent cache status retrieved successfully.",
		"data": [
			{"name": "movie.mkv", "size": 1, "hash": "abcdef1234567890"}
		]
	}`)

	cached, detail, err := parseTorboxCheckCachedResponse(body)
	if err != nil {
		t.Fatalf("parseTorboxCheckCachedResponse() error = %v", err)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	if !cached["abcdef1234567890"] {
		t.Fatal("expected array Torbox cache response to mark hash cached")
	}
}

func TestParseTorboxCheckCachedResponseNotCached(t *testing.T) {
	body := []byte(`{
		"success": true,
		"error": null,
		"detail": "Torrent cache status retrieved successfully.",
		"data": {}
	}`)

	cached, detail, err := parseTorboxCheckCachedResponse(body)
	if err != nil {
		t.Fatalf("parseTorboxCheckCachedResponse() error = %v", err)
	}
	if detail != "" {
		t.Fatalf("detail = %q, want empty", detail)
	}
	if len(cached) != 0 {
		t.Fatalf("cached len = %d, want 0", len(cached))
	}
}

func TestParseTorboxTorrentInfoResponseSingleObjectIgnoresUnstableFields(t *testing.T) {
	body := []byte(`{
		"success": true,
		"error": null,
		"detail": "Torrent list retrieved successfully.",
		"data": {
			"id": 74534407,
			"auth_id": "sensitive-account-id",
			"hash": "f3633cefd078e25b96978c4ae2f032f08403feab",
			"size": 14319331768,
			"download_state": "cached",
			"download_finished": "2026-08-11T15:58:24Z",
			"name": "Primal.S02.1080p.mkv",
			"files": [{"id": 2, "name": "Primal.S02E02.mkv", "size": 1234}]
		}
	}`)

	torrent, err := parseTorboxTorrentInfoResponse(body, "74534407")
	if err != nil {
		t.Fatalf("parseTorboxTorrentInfoResponse() error = %v", err)
	}
	if torrent.DownloadState != "cached" || len(torrent.Files) != 1 || torrent.Files[0].ID != 2 {
		t.Fatalf("unexpected torrent info: %+v", torrent)
	}
}

func TestParseTorboxTorrentInfoResponseArray(t *testing.T) {
	body := []byte(`{
		"success": true,
		"data": [
			{"id": 1, "download_state": "cached"},
			{"id": 2, "download_state": "completed", "name": "selected.mkv"}
		]
	}`)

	torrent, err := parseTorboxTorrentInfoResponse(body, "2")
	if err != nil {
		t.Fatalf("parseTorboxTorrentInfoResponse() error = %v", err)
	}
	if torrent.ID != 2 || torrent.Name != "selected.mkv" {
		t.Fatalf("unexpected torrent info: %+v", torrent)
	}
}

func TestParseTorboxTorrentInfoResponseDoesNotExposeRawBody(t *testing.T) {
	body := []byte(`{"success":true,"data":{"id":"bad","auth_id":"sensitive-account-id"}}`)

	_, err := parseTorboxTorrentInfoResponse(body, "1")
	if err == nil {
		t.Fatal("expected decode error")
	}
	if strings.Contains(err.Error(), "sensitive-account-id") {
		t.Fatalf("error exposed raw response body: %v", err)
	}
}
