package debrid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newPremiumizeTestClient(server *httptest.Server) *PremiumizeClient {
	client := NewPremiumizeClient("test-key")
	client.baseURL = server.URL + "/api"
	client.httpClient = server.Client()
	return client
}

func TestPremiumizeIsRegistered(t *testing.T) {
	provider, ok := GetProvider("premiumize", "key")
	if !ok {
		t.Fatal("premiumize provider is not registered")
	}
	if provider.Name() != "premiumize" {
		t.Fatalf("provider name = %q, want premiumize", provider.Name())
	}
}

func TestPremiumizeAddMagnetUsesBearerAndSourceForm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/transfer/create" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("src"); got != "magnet:?xt=urn:btih:abc" {
			t.Fatalf("src = %q", got)
		}
		_, _ = io.WriteString(w, `{"status":"success","id":"transfer-1","name":"Movie"}`)
	}))
	defer server.Close()

	result, err := newPremiumizeTestClient(server).AddMagnet(context.Background(), " magnet:?xt=urn:btih:abc ")
	if err != nil {
		t.Fatalf("AddMagnet returned error: %v", err)
	}
	if result.ID != "transfer-1" {
		t.Fatalf("ID = %q", result.ID)
	}
}

func TestPremiumizeAddTorrentFileUsesSrcMultipartField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("src")
		if err != nil {
			t.Fatalf("src form file: %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "movie.torrent" || string(data) != "torrent-data" {
			t.Fatalf("upload = %q %q", header.Filename, data)
		}
		_, _ = io.WriteString(w, `{"status":"success","id":"transfer-2"}`)
	}))
	defer server.Close()

	result, err := newPremiumizeTestClient(server).AddTorrentFile(context.Background(), []byte("torrent-data"), "movie.torrent")
	if err != nil {
		t.Fatalf("AddTorrentFile returned error: %v", err)
	}
	if result.ID != "transfer-2" {
		t.Fatalf("ID = %q", result.ID)
	}
}

func TestPremiumizeCheckInstantAvailabilityBulkPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cache/check" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"magnet:?xt=urn:btih:aaa",
			"magnet:?xt=urn:btih:bbb",
		}
		if got := r.Form["items[]"]; !reflect.DeepEqual(got, want) {
			t.Fatalf("items = %#v, want %#v", got, want)
		}
		_, _ = io.WriteString(w, `{"status":"success","response":[true,false],"filename":["A",null],"filesize":["1",0]}`)
	}))
	defer server.Close()

	got, err := newPremiumizeTestClient(server).CheckInstantAvailabilityBulk(context.Background(), []string{"AAA", "bbb", "aaa"})
	if err != nil {
		t.Fatalf("CheckInstantAvailabilityBulk returned error: %v", err)
	}
	if !got["aaa"] || got["bbb"] {
		t.Fatalf("availability = %#v", got)
	}
}

func TestPremiumizeGetTorrentInfoListsNestedFolderFilesDeterministically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/transfer/list":
			_, _ = io.WriteString(w, `{"status":"success","transfers":[{"id":"transfer-1","name":"Show","status":"seeding","progress":1,"folder_id":"root","file_id":null}]}`)
		case "/api/folder/list":
			switch r.URL.Query().Get("id") {
			case "root":
				_, _ = io.WriteString(w, `{"status":"success","content":[{"id":"f2","name":"z.mkv","type":"file","size":20,"mime_type":"video/x-matroska","link":"https://cdn.premiumize.me/z"},{"id":"season","name":"Season 1","type":"folder"}]}`)
			case "season":
				_, _ = io.WriteString(w, `{"status":"success","content":[{"id":"f1","name":"a.mkv","type":"file","size":10,"mime_type":"video/x-matroska","link":"https://cdn.premiumize.me/a"}]}`)
			default:
				t.Fatalf("unexpected folder id %q", r.URL.Query().Get("id"))
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	info, err := newPremiumizeTestClient(server).GetTorrentInfo(context.Background(), "transfer-1")
	if err != nil {
		t.Fatalf("GetTorrentInfo returned error: %v", err)
	}
	if info.Status != "downloaded" || info.Bytes != 30 {
		t.Fatalf("status/bytes = %q/%d", info.Status, info.Bytes)
	}
	if got := []string{info.Files[0].Path, info.Files[1].Path}; !reflect.DeepEqual(got, []string{"Season 1/a.mkv", "z.mkv"}) {
		t.Fatalf("paths = %#v", got)
	}
	if info.Files[0].ID != 1 || info.Files[1].ID != 2 {
		t.Fatalf("file IDs = %d, %d", info.Files[0].ID, info.Files[1].ID)
	}
	if got := info.Links; !reflect.DeepEqual(got, []string{"https://cdn.premiumize.me/a", "https://cdn.premiumize.me/z"}) {
		t.Fatalf("links = %#v", got)
	}
}

func TestPremiumizeGetTorrentInfoListsSingleFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/transfer/list":
			_, _ = io.WriteString(w, `{"status":"success","transfers":[{"id":"one","name":"Movie","status":"finished","file_id":"file-1"}]}`)
		case "/api/item/details":
			if r.URL.Query().Get("id") != "file-1" {
				t.Fatalf("id = %q", r.URL.Query().Get("id"))
			}
			_, _ = io.WriteString(w, `{"status":"success","id":"file-1","name":"Movie.mkv","size":42,"mime_type":"video/x-matroska","link":"https://cdn.premiumize.me/movie"}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	info, err := newPremiumizeTestClient(server).GetTorrentInfo(context.Background(), "one")
	if err != nil {
		t.Fatalf("GetTorrentInfo returned error: %v", err)
	}
	if len(info.Files) != 1 || info.Files[0].Path != "Movie.mkv" || info.Links[0] == "" {
		t.Fatalf("info = %#v", info)
	}
}

func TestPremiumizeAPIErrorEnvelopeIsReturnedAsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","message":"API key invalid","code":"authentication_failed"}`)
	}))
	defer server.Close()

	_, err := newPremiumizeTestClient(server).CheckInstantAvailability(context.Background(), "abc")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestPremiumizeDeleteTorrentSendsID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(data))
		if values.Get("id") != "transfer-1" {
			t.Fatalf("id = %q", values.Get("id"))
		}
		_, _ = io.WriteString(w, `{"status":"success"}`)
	}))
	defer server.Close()

	if err := newPremiumizeTestClient(server).DeleteTorrent(context.Background(), "transfer-1"); err != nil {
		t.Fatalf("DeleteTorrent returned error: %v", err)
	}
}

func TestPremiumizeGetAccountInfo(t *testing.T) {
	expires := time.Now().Add(48 * time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "success",
			"customer_id":   "12345",
			"premium_until": expires,
		})
	}))
	defer server.Close()

	info, err := newPremiumizeTestClient(server).GetAccountInfo(context.Background())
	if err != nil {
		t.Fatalf("GetAccountInfo returned error: %v", err)
	}
	if info.Username != "12345" || !info.PremiumActive || info.ExpiresAt == nil {
		t.Fatalf("account info = %#v", info)
	}
}
