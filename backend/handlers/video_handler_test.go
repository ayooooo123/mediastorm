package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novastream/config"
	"novastream/services/credits"
	"novastream/services/playback"
	"novastream/services/streaming"
)

type staticVideoConfigProvider struct {
	settings config.Settings
}

func (p staticVideoConfigProvider) Load() (config.Settings, error) {
	return p.settings, nil
}

// --- detectContainerExt tests ---

func TestDetectContainerExt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Direct suffix cases
		{name: "mp4 direct", input: "movie.mp4", expected: ".mp4"},
		{name: "mkv direct", input: "video.mkv", expected: ".mkv"},
		{name: "ts direct", input: "stream.ts", expected: ".ts"},
		{name: "m2ts direct", input: "bluray.m2ts", expected: ".m2ts"},
		{name: "avi direct", input: "old.avi", expected: ".avi"},
		{name: "webm direct", input: "web.webm", expected: ".webm"},
		{name: "m4v direct", input: "itunes.m4v", expected: ".m4v"},
		{name: "mpg direct", input: "dvd.mpg", expected: ".mpg"},
		{name: "mpeg direct", input: "dvd.mpeg", expected: ".mpeg"},
		{name: "m3u8 direct", input: "playlist.m3u8", expected: ".m3u8"},
		{name: "mts direct", input: "camcorder.mts", expected: ".mts"},

		// Case insensitive
		{name: "MKV uppercase", input: "MOVIE.MKV", expected: ".mkv"},
		{name: "MP4 uppercase", input: "VIDEO.MP4", expected: ".mp4"},
		{name: "Mixed case", input: "Video.Mp4", expected: ".mp4"},

		// Obfuscated filename patterns (usenet style)
		{name: "mkv with yEnc suffix", input: "file.mkv_yEnc_abc123", expected: ".mkv"},
		{name: "mp4 with extra extension", input: "movie.mp4.partial", expected: ".mp4"},
		{name: "mkv with dash suffix", input: "video.mkv-par2", expected: ".mkv"},

		// Path handling
		{name: "full path mkv", input: "/path/to/movie.mkv", expected: ".mkv"},
		{name: "nested path mp4", input: "/media/videos/2024/movie.mp4", expected: ".mp4"},

		// Edge cases
		{name: "empty string", input: "", expected: ""},
		{name: "whitespace only", input: "   ", expected: ""},
		{name: "no extension", input: "noextension", expected: ""},
		{name: "hidden file no ext", input: ".hidden", expected: ".hidden"},

		// Unknown extension fallback
		{name: "unknown extension", input: "file.xyz", expected: ".xyz"},
		{name: "txt file", input: "readme.txt", expected: ".txt"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := detectContainerExt(tc.input)
			if result != tc.expected {
				t.Errorf("detectContainerExt(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestProxyExternalURLUsesConfiguredUsenetWebDAVAuth(t *testing.T) {
	var sawAuth bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "webdav-user" || password != "webdav-pass" {
			t.Fatalf("missing configured WebDAV auth")
		}
		sawAuth = true
		w.Header().Set("Content-Type", "video/x-matroska")
		_, _ = io.WriteString(w, "video")
	}))
	defer upstream.Close()

	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:           "NZBDav",
		Type:           "nzbdav",
		Enabled:        true,
		BaseURL:        "http://engine.example",
		WebDAVBaseURL:  upstream.URL + "/webdav",
		WebDAVUsername: "webdav-user",
		WebDAVPassword: "webdav-pass",
	}}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path=x", nil)
	rec := httptest.NewRecorder()
	handled, err := handler.proxyExternalURL(rec, req, upstream.URL+"/webdav/Movie/Movie.mkv")
	if err != nil {
		t.Fatalf("proxyExternalURL: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !sawAuth {
		t.Fatal("upstream did not receive auth")
	}
}

func TestProxyExternalURLCachesFinalRedirectAndReusesConnection(t *testing.T) {
	var finalRequests atomic.Int32
	var finalConnections atomic.Int32
	finalServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalRequests.Add(1)
		if got := r.Header.Get("Range"); got != "bytes=0-1023" {
			t.Errorf("Range = %q, want bytes=0-1023", got)
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "video")
	}))
	finalServer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			finalConnections.Add(1)
		}
	}
	finalServer.Start()
	defer finalServer.Close()

	var redirectRequests atomic.Int32
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectRequests.Add(1)
		http.Redirect(w, r, finalServer.URL+"/movie.mkv", http.StatusFound)
	}))
	defer redirectServer.Close()

	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.Server.AllowedPrivateMediaOrigins = []string{redirectServer.URL, finalServer.URL}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/video/stream", nil)
		req.Header.Set("Range", "bytes=0-1023")
		rec := httptest.NewRecorder()
		handled, err := handler.proxyExternalURL(rec, req, redirectServer.URL+"/playback/token")
		if err != nil {
			t.Fatalf("proxyExternalURL: %v", err)
		}
		if !handled {
			t.Fatal("handled = false")
		}
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
	}

	if got := redirectRequests.Load(); got != 1 {
		t.Fatalf("redirect requests = %d, want 1", got)
	}
	if got := finalRequests.Load(); got != 2 {
		t.Fatalf("final requests = %d, want 2", got)
	}
	if got := finalConnections.Load(); got != 1 {
		t.Fatalf("final connections = %d, want 1 reused connection", got)
	}
}

func TestProxyExternalURLRejectsElfHostedPlaceholderAndInvalidatesPrequeue(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("custom transport should handle the request")
	}))
	defer origin.Close()

	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.Server.AllowedPrivateMediaOrigins = []string{origin.URL}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})
	handler.externalProxyHTTPClient = &http.Client{
		Transport: prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			finalReq := r.Clone(r.Context())
			finalReq.URL, _ = url.Parse("https://slate.elfhosted.com/cache/link-expired.mp4")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("placeholder")),
				Header:     make(http.Header),
				Request:    finalReq,
			}, nil
		}),
	}

	store := playback.NewPrequeueStore(time.Hour)
	entry, _ := store.Create("movie:1", "Movie", "default", "movie", 2020, nil, "details")
	store.Update(entry.ID, func(current *playback.PrequeueEntry) {
		current.Status = playback.PrequeueStatusReady
		current.StreamPath = origin.URL + "/playback/expired"
	})
	handler.SetPrequeueStore(store)

	req := httptest.NewRequest(http.MethodGet, "/video/stream", nil)
	rec := httptest.NewRecorder()
	handled, err := handler.proxyExternalURL(rec, req, entry.StreamPath)
	if !handled {
		t.Fatal("handled = false")
	}
	if !errors.Is(err, errExternalStreamPlaceholder) {
		t.Fatalf("proxyExternalURL error = %v, want external placeholder error", err)
	}
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}
	if _, ok := store.Get(entry.ID); ok {
		t.Fatal("expired placeholder prequeue was not invalidated")
	}
}

func TestExternalProxyResumeRangeHeader(t *testing.T) {
	if got := externalProxyResumeRangeHeader(0, 0, false, 100); got != "bytes=100-" {
		t.Fatalf("open-ended from zero: got %q", got)
	}
	if got := externalProxyResumeRangeHeader(1000, 5000, true, 250); got != "bytes=1250-5000" {
		t.Fatalf("finite range: got %q", got)
	}
	if got := externalProxyResumeRangeHeader(100, 200, true, 101); got != "" {
		t.Fatalf("exhausted finite range: got %q, want empty", got)
	}
}

func TestIsRecoverableExternalProxyReadError(t *testing.T) {
	if isRecoverableExternalProxyReadError(io.EOF) {
		t.Fatal("clean EOF should not be recoverable")
	}
	if !isRecoverableExternalProxyReadError(io.ErrUnexpectedEOF) {
		t.Fatal("unexpected EOF should be recoverable")
	}
	if !isRecoverableExternalProxyReadError(errors.New("read tcp 1.2.3.4:443: connection reset by peer")) {
		t.Fatal("connection reset should be recoverable")
	}
	if isRecoverableExternalProxyReadError(errors.New("certificate invalid")) {
		t.Fatal("non-transport errors should not be recoverable")
	}
}

func TestProxyExternalURLReconnectsAfterUpstreamUnexpectedEOF(t *testing.T) {
	payload := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghij")
	splitAt := 16
	var upstreamRequests atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := upstreamRequests.Add(1)
		rangeHdr := r.Header.Get("Range")
		start, end, hasEnd, ok := parseSingleByteRange(rangeHdr)
		if !ok {
			start = 0
		}
		if !hasEnd {
			end = int64(len(payload) - 1)
		}
		if start < 0 || start >= int64(len(payload)) {
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(len(payload)) {
			end = int64(len(payload) - 1)
		}

		// First open: advertise the full remaining body, write only a prefix,
		// then hard-close so the proxy sees unexpected EOF (idle CDN kill).
		if n == 1 {
			if start != 0 {
				t.Fatalf("first request start = %d, want 0", start)
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support Hijack")
				return
			}
			conn, bufrw, err := hj.Hijack()
			if err != nil {
				t.Fatalf("Hijack: %v", err)
				return
			}
			chunk := payload[:splitAt]
			// Claim the full file length so the client still expects more bytes
			// when we drop the connection after the prefix.
			_, _ = fmt.Fprintf(bufrw,
				"HTTP/1.1 206 Partial Content\r\n"+
					"Content-Type: video/x-matroska\r\n"+
					"Accept-Ranges: bytes\r\n"+
					"Content-Range: bytes 0-%d/%d\r\n"+
					"Content-Length: %d\r\n"+
					"Connection: close\r\n"+
					"\r\n",
				len(payload)-1, len(payload), len(payload))
			_, _ = bufrw.Write(chunk)
			_ = bufrw.Flush()
			_ = conn.Close()
			return
		}

		// Reconnect: serve from the requested offset to EOF.
		if start != int64(splitAt) {
			t.Errorf("reconnect Range start = %d, want %d (header=%q)", start, splitAt, rangeHdr)
		}
		body := payload[start : end+1]
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.Server.AllowedPrivateMediaOrigins = []string{upstream.URL}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	req := httptest.NewRequest(http.MethodGet, "/video/stream", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	handled, err := handler.proxyExternalURL(rec, req, upstream.URL+"/Silo.S03E06.mkv")
	if err != nil {
		t.Fatalf("proxyExternalURL: %v", err)
	}
	if !handled {
		t.Fatal("handled = false")
	}
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if got := upstreamRequests.Load(); got < 2 {
		t.Fatalf("upstream requests = %d, want at least 2 (initial + reconnect)", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("body mismatch: got %q want %q", rec.Body.Bytes(), payload)
	}
}

func TestProxyExternalURLStartupExperimentServesRepeatedRangeFromPrefixSpool(t *testing.T) {
	payload := bytes.Repeat([]byte("matroska"), 256)
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.Server.AllowedPrivateMediaOrigins = []string{upstream.URL}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	firstReq := httptest.NewRequest(http.MethodGet, "/video/stream?_startupExperiment=1", nil)
	firstReq.Header.Set("Range", "bytes=0-")
	firstRec := httptest.NewRecorder()
	if handled, err := handler.proxyExternalURL(firstRec, firstReq, upstream.URL+"/movie.mkv"); err != nil || !handled {
		t.Fatalf("first proxyExternalURL: handled=%t err=%v", handled, err)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/video/stream?_startupExperiment=1", nil)
	secondReq.Header.Set("Range", "bytes=337-")
	secondRec := httptest.NewRecorder()
	if handled, err := handler.proxyExternalURL(secondRec, secondReq, upstream.URL+"/movie.mkv"); err != nil || !handled {
		t.Fatalf("second proxyExternalURL: handled=%t err=%v", handled, err)
	}

	if got := upstreamRequests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	if got := secondRec.Header().Get("X-Strmr-Startup-Spool"); got != "hit" {
		t.Fatalf("X-Strmr-Startup-Spool = %q, want hit", got)
	}
	if got := secondRec.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 337-%d/%d", len(payload)-1, len(payload)) {
		t.Fatalf("Content-Range = %q", got)
	}
	if !bytes.Equal(secondRec.Body.Bytes(), payload[337:]) {
		t.Fatal("spooled proxy response mismatch")
	}
}

func TestExternalUsenetWebDAVAuthHeaderForFFProbe(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:           "AltMount",
		Type:           "altmount",
		Enabled:        true,
		WebDAVBaseURL:  "http://127.0.0.1:3313/webdav",
		WebDAVUsername: "webdav-user",
		WebDAVPassword: "webdav-pass",
	}}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	got := handler.externalUsenetWebDAVAuthHeader("http://127.0.0.1:3313/webdav/Default/complete/Movie/Movie.mkv")
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("webdav-user:webdav-pass")) + "\r\n"
	if got != want {
		t.Fatalf("auth header = %q, want %q", got, want)
	}
}

func TestExternalUsenetWebDAVAuthHeaderIgnoresOtherURLs(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	settings := config.DefaultSettings()
	settings.UsenetEngines = []config.UsenetEngineSettings{{
		Name:           "AltMount",
		Type:           "altmount",
		Enabled:        true,
		WebDAVBaseURL:  "http://127.0.0.1:3313/webdav",
		WebDAVUsername: "webdav-user",
		WebDAVPassword: "webdav-pass",
	}}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	if got := handler.externalUsenetWebDAVAuthHeader("https://example.com/video.mkv"); got != "" {
		t.Fatalf("auth header for unrelated URL = %q, want empty", got)
	}
}

// --- detectDolbyVision tests ---

func TestDetectDolbyVision(t *testing.T) {
	tests := []struct {
		name            string
		stream          *ffprobeStream
		expectHasDV     bool
		expectDVProfile string
		expectHDRFormat string
	}{
		{
			name:            "nil stream",
			stream:          nil,
			expectHasDV:     false,
			expectDVProfile: "",
			expectHDRFormat: "",
		},
		{
			name: "non-HEVC codec",
			stream: &ffprobeStream{
				CodecName: "h264",
			},
			expectHasDV:     false,
			expectDVProfile: "",
			expectHDRFormat: "",
		},
		{
			name: "HEVC without DV",
			stream: &ffprobeStream{
				CodecName: "hevc",
			},
			expectHasDV:     false,
			expectDVProfile: "",
			expectHDRFormat: "",
		},
		{
			name: "HEVC with HDR10 (PQ transfer)",
			stream: &ffprobeStream{
				CodecName:     "hevc",
				ColorTransfer: "smpte2084",
			},
			expectHasDV:     false,
			expectDVProfile: "",
			expectHDRFormat: "HDR10",
		},
		{
			name: "HEVC with HLG",
			stream: &ffprobeStream{
				CodecName:     "hevc",
				ColorTransfer: "arib-std-b67",
			},
			expectHasDV:     false,
			expectDVProfile: "",
			expectHDRFormat: "HLG",
		},
		{
			name: "HEVC with DV side data profile 8",
			stream: &ffprobeStream{
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{
					{
						SideDataType:              "DOVI configuration record",
						DVProfile:                 8,
						DVLevel:                   6,
						DVBLSignalCompatibilityID: 1,
					},
				},
			},
			expectHasDV:     true,
			expectDVProfile: "dvhe.08.06",
			expectHDRFormat: "DV",
		},
		{
			name: "HEVC with DV side data profile 5",
			stream: &ffprobeStream{
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{
					{
						SideDataType: "dolby vision",
						DVProfile:    5,
						DVLevel:      9,
					},
				},
			},
			expectHasDV:     true,
			expectDVProfile: "dvhe.05.09",
			expectHDRFormat: "DV",
		},
		{
			name: "HEVC with DV profile 7",
			stream: &ffprobeStream{
				CodecName: "hevc",
				SideDataList: []ffprobeSideData{
					{
						SideDataType: "dovi config",
						DVProfile:    7,
						DVLevel:      6,
					},
				},
			},
			expectHasDV:     true,
			expectDVProfile: "dvhe.07.06",
			expectHDRFormat: "DV",
		},
		{
			name: "HEVC with DV via profile string",
			stream: &ffprobeStream{
				CodecName: "hevc",
				Profile:   "dvhe.08.06",
			},
			expectHasDV:     true,
			expectDVProfile: "dvhe.08.06",
			expectHDRFormat: "DV",
		},
		{
			name: "H265 alias with DV side data",
			stream: &ffprobeStream{
				CodecName: "h265",
				SideDataList: []ffprobeSideData{
					{
						SideDataType: "DOVI",
						DVProfile:    8,
						DVLevel:      4,
					},
				},
			},
			expectHasDV:     true,
			expectDVProfile: "dvhe.08.04",
			expectHDRFormat: "DV",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hasDV, dvProfile, hdrFormat := detectDolbyVision(tc.stream)
			if hasDV != tc.expectHasDV {
				t.Errorf("hasDV = %v, want %v", hasDV, tc.expectHasDV)
			}
			if dvProfile != tc.expectDVProfile {
				t.Errorf("dvProfile = %q, want %q", dvProfile, tc.expectDVProfile)
			}
			if hdrFormat != tc.expectHDRFormat {
				t.Errorf("hdrFormat = %q, want %q", hdrFormat, tc.expectHDRFormat)
			}
		})
	}
}

func TestComposeMetadataResponseIncludesDolbyVisionConfiguration(t *testing.T) {
	meta := &ffprobeOutput{
		Streams: []ffprobeStream{
			{
				Index:     2,
				CodecType: "video",
				CodecName: "hevc",
				PixFmt:    "yuv420p10le",
				SideDataList: []ffprobeSideData{
					{
						SideDataType:              "DOVI configuration record",
						DVVersionMajor:            1,
						DVVersionMinor:            0,
						DVProfile:                 8,
						DVLevel:                   6,
						RPUPresentFlag:            1,
						ELPresentFlag:             0,
						BLPresentFlag:             1,
						DVBLSignalCompatibilityID: 1,
					},
				},
			},
		},
	}

	response := composeMetadataResponse(meta, "/movie.mkv", audioPlan{})
	if len(response.VideoStreams) != 1 {
		t.Fatalf("video stream count = %d, want 1", len(response.VideoStreams))
	}
	stream := response.VideoStreams[0]
	if stream.DolbyVisionConfiguration == nil {
		t.Fatal("DolbyVisionConfiguration is nil")
	}
	config := stream.DolbyVisionConfiguration
	if config.StreamIndex != 2 || config.PixelFormat != "yuv420p10le" ||
		config.VersionMajor != 1 || config.VersionMinor != 0 ||
		config.Profile != 8 || config.Level != 6 ||
		config.RPUPresentFlag != 1 || config.ELPresentFlag != 0 ||
		config.BLPresentFlag != 1 || config.BLSignalCompatibilityID != 1 {
		t.Fatalf("unexpected Dolby Vision configuration: %+v", config)
	}
}

// --- isDolbyVisionProfile7 tests ---

func TestIsDolbyVisionProfile7(t *testing.T) {
	tests := []struct {
		name     string
		profile  string
		expected bool
	}{
		{name: "empty string", profile: "", expected: false},
		{name: "profile 8", profile: "dvhe.08.06", expected: false},
		{name: "profile 5", profile: "dvhe.05.09", expected: false},
		{name: "profile 7 standard format", profile: "dvhe.07.06", expected: true},
		{name: "profile 7 level 9", profile: "dvhe.07.09", expected: true},
		{name: "profile 7 uppercase", profile: "DVHE.07.06", expected: true},
		{name: "profile 7 text format", profile: "profile 7", expected: true},
		{name: "p7 shorthand", profile: "p7", expected: true},
		{name: "profile 8 not 7", profile: "profile 8", expected: false},
		{name: "whitespace handling", profile: "  dvhe.07.06  ", expected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isDolbyVisionProfile7(tc.profile)
			if result != tc.expected {
				t.Errorf("isDolbyVisionProfile7(%q) = %v, want %v", tc.profile, result, tc.expected)
			}
		})
	}
}

// --- shouldTagHevcAsHvc1 tests ---

func TestShouldTagHevcAsHvc1(t *testing.T) {
	tests := []struct {
		name     string
		codec    string
		expected bool
	}{
		{name: "empty codec", codec: "", expected: false},
		{name: "hevc lowercase", codec: "hevc", expected: true},
		{name: "HEVC uppercase", codec: "HEVC", expected: true},
		{name: "h265 lowercase", codec: "h265", expected: true},
		{name: "H265 uppercase", codec: "H265", expected: true},
		{name: "hevc with prefix", codec: "hevc_main", expected: true},
		{name: "h264 not hevc", codec: "h264", expected: false},
		{name: "avc not hevc", codec: "avc1", expected: false},
		{name: "vp9 not hevc", codec: "vp9", expected: false},
		{name: "whitespace handling", codec: "  hevc  ", expected: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldTagHevcAsHvc1(tc.codec)
			if result != tc.expected {
				t.Errorf("shouldTagHevcAsHvc1(%q) = %v, want %v", tc.codec, result, tc.expected)
			}
		})
	}
}

// --- selectPrimaryVideoStream tests ---

func TestSelectPrimaryVideoStream(t *testing.T) {
	tests := []struct {
		name          string
		meta          *ffprobeOutput
		expectedIndex int // -1 means nil expected
	}{
		{
			name:          "nil metadata",
			meta:          nil,
			expectedIndex: -1,
		},
		{
			name: "empty streams",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{},
			},
			expectedIndex: -1,
		},
		{
			name: "single video stream",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "h264"},
				},
			},
			expectedIndex: 0,
		},
		{
			name: "video and audio streams",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "hevc"},
					{Index: 1, CodecType: "audio", CodecName: "aac"},
				},
			},
			expectedIndex: 0,
		},
		{
			name: "audio first then video",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "ac3"},
					{Index: 1, CodecType: "video", CodecName: "h264"},
					{Index: 2, CodecType: "subtitle", CodecName: "subrip"},
				},
			},
			expectedIndex: 1,
		},
		{
			name: "multiple video streams - picks first",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "h264"},
					{Index: 1, CodecType: "video", CodecName: "hevc"},
				},
			},
			expectedIndex: 0,
		},
		{
			name: "no video streams",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "aac"},
					{Index: 1, CodecType: "subtitle", CodecName: "ass"},
				},
			},
			expectedIndex: -1,
		},
		{
			name: "case insensitive codec type",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "VIDEO", CodecName: "h264"},
				},
			},
			expectedIndex: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := selectPrimaryVideoStream(tc.meta)
			if tc.expectedIndex == -1 {
				if result != nil {
					t.Errorf("expected nil, got stream with index %d", result.Index)
				}
			} else {
				if result == nil {
					t.Errorf("expected stream with index %d, got nil", tc.expectedIndex)
				} else if result.Index != tc.expectedIndex {
					t.Errorf("expected stream index %d, got %d", tc.expectedIndex, result.Index)
				}
			}
		})
	}
}

// --- determineAudioPlan tests ---

func TestDetermineAudioPlan(t *testing.T) {
	tests := []struct {
		name         string
		meta         *ffprobeOutput
		forceAAC     bool
		expectedMode audioPlanMode
	}{
		{
			name:         "nil metadata no force AAC",
			meta:         nil,
			forceAAC:     false,
			expectedMode: audioPlanNone,
		},
		{
			name:         "nil metadata with force AAC",
			meta:         nil,
			forceAAC:     true,
			expectedMode: audioPlanTranscode,
		},
		{
			name: "AAC audio without force",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "aac"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanCopy,
		},
		{
			name: "AAC audio with force AAC",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "aac"},
				},
			},
			forceAAC:     true,
			expectedMode: audioPlanCopy,
		},
		{
			name: "AC3 audio without force",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "ac3"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanCopy,
		},
		{
			name: "AC3 audio with force AAC",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "ac3"},
				},
			},
			forceAAC:     true,
			expectedMode: audioPlanTranscode,
		},
		{
			name: "EAC3 audio without force",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "eac3"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanCopy,
		},
		{
			name: "MP3 audio without force",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "mp3"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanCopy,
		},
		{
			name: "DTS audio requires transcode",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "dts"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanTranscode,
		},
		{
			name: "TrueHD audio requires transcode",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "truehd"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanTranscode,
		},
		{
			name: "FLAC audio requires transcode",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "flac"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanTranscode,
		},
		{
			name: "Multiple audio - picks first copyable",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "h264"},
					{Index: 1, CodecType: "audio", CodecName: "ac3"},
					{Index: 2, CodecType: "audio", CodecName: "aac"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanCopy,
		},
		{
			name: "Force AAC finds AAC track",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "audio", CodecName: "ac3"},
					{Index: 1, CodecType: "audio", CodecName: "aac"},
				},
			},
			forceAAC:     true,
			expectedMode: audioPlanCopy, // Found AAC track
		},
		{
			name: "No audio streams",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "h264"},
				},
			},
			forceAAC:     false,
			expectedMode: audioPlanNone,
		},
		{
			name: "No audio with force AAC",
			meta: &ffprobeOutput{
				Streams: []ffprobeStream{
					{Index: 0, CodecType: "video", CodecName: "h264"},
				},
			},
			forceAAC:     true,
			expectedMode: audioPlanTranscode,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := determineAudioPlan(tc.meta, tc.forceAAC)
			if result.mode != tc.expectedMode {
				t.Errorf("determineAudioPlan() mode = %q, want %q (reason: %s)", result.mode, tc.expectedMode, result.reason)
			}
		})
	}
}

// --- audioPlan.codec tests ---

func TestAudioPlanCodec(t *testing.T) {
	tests := []struct {
		name     string
		plan     audioPlan
		expected string
	}{
		{
			name:     "nil stream",
			plan:     audioPlan{mode: audioPlanNone, stream: nil},
			expected: "",
		},
		{
			name: "AAC stream",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "aac"},
			},
			expected: "aac",
		},
		{
			name: "AC3 stream with whitespace",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "  AC3  "},
			},
			expected: "ac3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.plan.codec()
			if result != tc.expected {
				t.Errorf("audioPlan.codec() = %q, want %q", result, tc.expected)
			}
		})
	}
}

// --- computeMovflags tests ---

func TestComputeMovflags(t *testing.T) {
	tests := []struct {
		name     string
		plan     audioPlan
		contains []string
		excludes []string
	}{
		{
			name: "no audio plan",
			plan: audioPlan{mode: audioPlanNone},
			contains: []string{
				"frag_keyframe",
				"separate_moof",
				"omit_tfhd_offset",
				"default_base_moof",
				"empty_moov",
			},
		},
		{
			name: "AAC copy",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "aac"},
			},
			contains: []string{"frag_keyframe", "empty_moov"},
		},
		{
			name: "AC3 copy - no empty_moov",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "ac3"},
			},
			contains: []string{"frag_keyframe"},
			excludes: []string{"empty_moov"},
		},
		{
			name: "EAC3 copy - no empty_moov",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "eac3"},
			},
			contains: []string{"frag_keyframe"},
			excludes: []string{"empty_moov"},
		},
		{
			name: "transcode mode",
			plan: audioPlan{mode: audioPlanTranscode},
			contains: []string{
				"frag_keyframe",
				"empty_moov",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := computeMovflags(tc.plan)
			for _, want := range tc.contains {
				if !contains(result, want) {
					t.Errorf("computeMovflags() = %q, want to contain %q", result, want)
				}
			}
			for _, notWant := range tc.excludes {
				if contains(result, notWant) {
					t.Errorf("computeMovflags() = %q, should NOT contain %q", result, notWant)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- shouldIncludeEmptyMoov tests ---

func TestShouldIncludeEmptyMoov(t *testing.T) {
	tests := []struct {
		name     string
		plan     audioPlan
		expected bool
	}{
		{
			name:     "none mode",
			plan:     audioPlan{mode: audioPlanNone},
			expected: true,
		},
		{
			name:     "transcode mode",
			plan:     audioPlan{mode: audioPlanTranscode},
			expected: true,
		},
		{
			name: "copy AAC",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "aac"},
			},
			expected: true,
		},
		{
			name: "copy AC3",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "ac3"},
			},
			expected: false,
		},
		{
			name: "copy EAC3",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "eac3"},
			},
			expected: false,
		},
		{
			name: "copy MP3",
			plan: audioPlan{
				mode:   audioPlanCopy,
				stream: &ffprobeStream{CodecName: "mp3"},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shouldIncludeEmptyMoov(tc.plan)
			if result != tc.expected {
				t.Errorf("shouldIncludeEmptyMoov() = %v, want %v", result, tc.expected)
			}
		})
	}
}

// --- VideoHandler HTTP tests ---

// testStreamProvider implements streaming.Provider for testing
type testStreamProvider struct {
	data        []byte
	statusCode  int
	headers     http.Header
	err         error
	hadDeadline bool
}

func (p *testStreamProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	_, p.hadDeadline = ctx.Deadline()
	if p.err != nil {
		return nil, p.err
	}
	headers := p.headers
	if headers == nil {
		headers = make(http.Header)
		headers.Set("Content-Type", "video/x-matroska")
		headers.Set("Accept-Ranges", "bytes")
	}
	status := p.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(p.data)),
		Headers:       headers,
		Status:        status,
		ContentLength: int64(len(p.data)),
	}, nil
}

func TestVideoHandler_StreamVideo_MissingPath(t *testing.T) {
	handler := NewVideoHandler(false, "", "")

	req := httptest.NewRequest(http.MethodGet, "/video/stream", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestVideoHandler_StreamVideo_MethodNotAllowed(t *testing.T) {
	handler := NewVideoHandler(false, "", "")

	req := httptest.NewRequest(http.MethodPost, "/video/stream?path=test.mkv", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestVideoHandler_StreamVideo_NoProvider(t *testing.T) {
	handler := NewVideoHandler(false, "", "")

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path=test.mkv", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rr.Code)
	}
}

func TestVideoHandler_StreamVideo_Success(t *testing.T) {
	data := []byte("test video data")
	provider := &testStreamProvider{data: data}
	handler := NewVideoHandlerWithProvider(false, "", "", "", provider)

	req := httptest.NewRequest(http.MethodGet, "/video/stream?path=test.mp4", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	body := rr.Body.Bytes()
	if !bytes.Equal(body, data) {
		t.Errorf("body = %q, want %q", body, data)
	}
	if provider.hadDeadline {
		t.Fatal("provider context has a deadline; playback must remain open until the client disconnects")
	}
}

func TestProxyExternalURLHasNoWholePlaybackTimeout(t *testing.T) {
	const origin = "http://127.0.0.1:3312"

	handler := NewVideoHandler(false, "", "")
	if handler.externalProxyHTTPClient.Timeout != 0 {
		t.Fatalf("external proxy client timeout = %v, want no whole-request timeout", handler.externalProxyHTTPClient.Timeout)
	}

	settings := config.DefaultSettings()
	settings.Server.AllowedPrivateMediaOrigins = []string{origin}
	handler.SetConfigManager(staticVideoConfigProvider{settings: settings})

	var upstreamHadDeadline bool
	handler.externalProxyHTTPClient = &http.Client{
		Transport: prequeueRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			_, upstreamHadDeadline = r.Context().Deadline()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("video")),
				Header: http.Header{
					"Content-Type":   []string{"video/x-matroska"},
					"Content-Length": []string{"5"},
				},
				Request: r,
			}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodGet, "/video/stream", nil)
	rec := httptest.NewRecorder()
	handled, err := handler.proxyExternalURL(rec, req, origin+"/webdav/movie.mkv")
	if err != nil || !handled {
		t.Fatalf("proxyExternalURL: handled=%t err=%v", handled, err)
	}
	if upstreamHadDeadline {
		t.Fatal("external proxy request has a deadline; response-body streaming would be cut off")
	}
	if got := rec.Body.String(); got != "video" {
		t.Fatalf("body = %q, want %q", got, "video")
	}
}

func TestVideoHandler_StreamVideo_CleanPath(t *testing.T) {
	tests := []struct {
		name         string
		inputPath    string
		expectedPath string
	}{
		{
			name:         "webdav prefix with leading slash",
			inputPath:    "/webdav/movies/test.mkv",
			expectedPath: "/movies/test.mkv",
		},
		{
			name:         "webdav prefix without leading slash",
			inputPath:    "webdav/movies/test.mkv",
			expectedPath: "/movies/test.mkv",
		},
		{
			name:         "no webdav prefix",
			inputPath:    "movies/test.mkv",
			expectedPath: "movies/test.mkv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedPath string
			provider := &pathCapturingProvider{
				capturePath: func(p string) {
					capturedPath = p
				},
				data: []byte("test"),
			}
			handler := NewVideoHandlerWithProvider(false, "", "", "", provider)

			req := httptest.NewRequest(http.MethodGet, "/video/stream?path="+tc.inputPath, nil)
			rr := httptest.NewRecorder()

			handler.StreamVideo(rr, req)

			if capturedPath != tc.expectedPath {
				t.Errorf("captured path = %q, want %q", capturedPath, tc.expectedPath)
			}
		})
	}
}

// pathCapturingProvider captures the path passed to Stream for testing
type pathCapturingProvider struct {
	capturePath func(string)
	data        []byte
}

func (p *pathCapturingProvider) Stream(ctx context.Context, req streaming.Request) (*streaming.Response, error) {
	if p.capturePath != nil {
		p.capturePath(req.Path)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "video/mp4")
	return &streaming.Response{
		Body:          io.NopCloser(bytes.NewReader(p.data)),
		Headers:       headers,
		Status:        http.StatusOK,
		ContentLength: int64(len(p.data)),
	}, nil
}

func TestVideoHandler_HandleOptions(t *testing.T) {
	handler := NewVideoHandler(false, "", "")

	req := httptest.NewRequest(http.MethodOptions, "/video/stream", nil)
	rr := httptest.NewRecorder()

	handler.HandleOptions(rr, req)

	// HandleOptions returns 200 OK (not 204 No Content)
	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// Check CORS headers
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected Access-Control-Allow-Origin: *")
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected Access-Control-Allow-Methods header")
	}
	if rr.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("expected Access-Control-Allow-Headers header")
	}
	if rr.Header().Get("Access-Control-Expose-Headers") == "" {
		t.Error("expected Access-Control-Expose-Headers header")
	}
}

func TestVideoHandler_StreamVideo_HeadRequest(t *testing.T) {
	data := []byte("test video data")
	provider := &testStreamProvider{data: data}
	handler := NewVideoHandlerWithProvider(false, "", "", "", provider)

	req := httptest.NewRequest(http.MethodHead, "/video/stream?path=test.mp4", nil)
	rr := httptest.NewRecorder()

	handler.StreamVideo(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	// HEAD request should not have body
	if rr.Body.Len() != 0 {
		t.Errorf("HEAD request should have empty body, got %d bytes", rr.Body.Len())
	}
}

// --- isConnectionError tests ---

func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "context canceled - not detected by string",
			err:      context.Canceled,
			expected: false, // isConnectionError uses string matching, not errors.Is
		},
		{
			name:     "context deadline exceeded",
			err:      context.DeadlineExceeded,
			expected: true, // net.Error with Timeout() = true
		},
		{
			name:     "generic error",
			err:      io.EOF,
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isConnectionError(tc.err)
			if result != tc.expected {
				t.Errorf("isConnectionError(%v) = %v, want %v", tc.err, result, tc.expected)
			}
		})
	}
}

func TestIsConnectionError_StringPatterns(t *testing.T) {
	// Test error message patterns that isConnectionError detects
	patterns := []string{
		"connection reset by peer",
		"broken pipe",
		"connection refused",
		"connection aborted",
		"connection timed out",
		"use of closed network connection",
		"write: connection reset",
		"read: connection reset",
	}

	for _, pattern := range patterns {
		t.Run(pattern, func(t *testing.T) {
			err := fmt.Errorf("error: %s happened", pattern)
			if !isConnectionError(err) {
				t.Errorf("isConnectionError should detect %q pattern", pattern)
			}
		})
	}
}

// --- Credits detection handler tests ---

func TestVideoHandler_DetectCredits_Disabled(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	// Detection is always enabled when a detector is configured; without one it is disabled.

	req := httptest.NewRequest(http.MethodPost, "/video/credits/detect?path=test.mkv&duration=3600", nil)
	rr := httptest.NewRecorder()

	handler.DetectCredits(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !bytes.Contains([]byte(body), []byte(`"status":"disabled"`)) {
		t.Errorf("expected disabled status, got %s", body)
	}
}

func TestVideoHandler_DetectCredits_MissingPath(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.creditsDetector = newTestCreditsDetector()

	req := httptest.NewRequest(http.MethodPost, "/video/credits/detect?duration=3600", nil)
	rr := httptest.NewRecorder()

	handler.DetectCredits(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestVideoHandler_DetectCredits_MissingDuration(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.creditsDetector = newTestCreditsDetector()

	req := httptest.NewRequest(http.MethodPost, "/video/credits/detect?path=test.mkv", nil)
	rr := httptest.NewRecorder()

	handler.DetectCredits(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestVideoHandler_GetCreditsStatus_Disabled(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	// Detection is always enabled when a detector is configured; without one it is disabled.

	req := httptest.NewRequest(http.MethodGet, "/video/credits/status?path=test.mkv", nil)
	rr := httptest.NewRecorder()

	handler.GetCreditsStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !bytes.Contains([]byte(body), []byte(`"status":"disabled"`)) {
		t.Errorf("expected disabled status, got %s", body)
	}
}

func TestVideoHandler_GetCreditsStatus_MissingPath(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.creditsDetector = newTestCreditsDetector()

	req := httptest.NewRequest(http.MethodGet, "/video/credits/status", nil)
	rr := httptest.NewRecorder()

	handler.GetCreditsStatus(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestVideoHandler_GetCreditsStatus_Pending(t *testing.T) {
	handler := NewVideoHandler(false, "", "")
	handler.creditsDetector = newTestCreditsDetector()

	req := httptest.NewRequest(http.MethodGet, "/video/credits/status?path=unknown/path", nil)
	rr := httptest.NewRecorder()

	handler.GetCreditsStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !bytes.Contains([]byte(body), []byte(`"status":"pending"`)) {
		t.Errorf("expected pending status, got %s", body)
	}
}

func newTestCreditsDetector() *credits.Detector {
	return credits.NewDetector()
}
