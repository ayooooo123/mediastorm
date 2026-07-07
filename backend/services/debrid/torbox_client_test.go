package debrid

import "testing"

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
