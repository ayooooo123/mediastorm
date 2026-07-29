package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
)

func TestNotificationsTemplateLoads(t *testing.T) {
	handler := NewAdminUIHandler("", "", nil, nil, nil, nil)
	if handler.notificationsTemplate == nil {
		t.Fatal("notifications template failed to load")
	}
}

func TestNotificationsTemplateDoesNotRedeclareBasePath(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/notifications.html")
	if err != nil {
		t.Fatalf("read notifications template: %v", err)
	}
	if strings.Contains(string(templateBytes), "const basePath =") {
		t.Fatal("notifications template redeclares the base template's global basePath")
	}
}

func TestNotificationsTemplateOmitsRedundantPlayingEvent(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/notifications.html")
	if err != nil {
		t.Fatalf("read notifications template: %v", err)
	}
	source := string(templateBytes)
	if strings.Contains(source, `value="watch.playing"`) {
		t.Fatal("notifications template still exposes the redundant playing event")
	}
	if strings.Contains(source, "Now playing") {
		t.Fatal("notifications template still labels a playing notification")
	}
}

func TestNotificationsTemplateIncludesSystemOperationsSection(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/notifications.html")
	if err != nil {
		t.Fatalf("read notifications template: %v", err)
	}
	source := string(templateBytes)
	for _, marker := range []string{
		"System Operations",
		`value="system.startup"`,
		`value="system.shutdown"`,
		`id="system-settings"`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("notifications template missing system operations marker %q", marker)
		}
	}
}

func TestNotificationListDisablesCaching(t *testing.T) {
	handler := &AdminUIHandler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/notifications?profileId=profile", nil)

	handler.ListNotificationChannels(recorder, request)

	if got := recorder.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q, want no-store, max-age=0", got)
	}
}

func TestAdminSettingsSaveCommitsPendingTextArrayInputs(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`data-text-array-kind="tags"`,
		`data-text-array-kind="weighted-tags"`,
		"function commitPendingTextArrayInputs()",
		"if (committedPendingTextArrays) renderSettings();",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing pending text-array marker %q", marker)
		}
	}

	for _, saveFunction := range []string{"saveSection", "saveAllSettings"} {
		start := strings.Index(source, "async function "+saveFunction+"(")
		if start < 0 {
			t.Fatalf("settings template missing %s", saveFunction)
		}
		body := source[start:]
		commit := strings.Index(body, "commitPendingTextArrayInputs();")
		serialize := strings.Index(body, "JSON.stringify(")
		if commit < 0 || serialize < 0 || commit > serialize {
			t.Fatalf("%s must commit pending text-array inputs before serializing settings", saveFunction)
		}
	}
}

func TestAdminSettingsCustomShelfActionsAlignWithInputs(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		".add-custom-list-form .form-group{flex:1;margin-bottom:0;}",
		".tmdb-source-actions button,.add-custom-list-submit{height:38px;display:inline-flex;align-items:center;justify-content:center;}",
		"new URLSearchParams(window.location.search).get('layoutDebug') === '1'",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing custom shelf alignment marker %q", marker)
		}
	}
}

func TestUsenetEngineStatusProbeJobIDUsesGUIDForNZBDav(t *testing.T) {
	for _, engineType := range []string{"nzbdav", "nzbdavex"} {
		t.Run(engineType, func(t *testing.T) {
			got := usenetEngineStatusProbeJobID(config.UsenetEngineSettings{Type: engineType})
			if got != "00000000-0000-4000-8000-000000000000" {
				t.Fatalf("probe job id = %q, want GUID-shaped placeholder", got)
			}
		})
	}

	got := usenetEngineStatusProbeJobID(config.UsenetEngineSettings{Type: "altmount"})
	if !strings.HasPrefix(got, "strmr-connection-test-") {
		t.Fatalf("altmount probe job id = %q, want legacy prefix", got)
	}
}

func TestExplainUsenetEngineRemoteConfigMismatchDetectsDecypharrCustomFolder(t *testing.T) {
	webdav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/webdav/mediastorm/</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
        <d:displayname>mediastorm</d:displayname>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
	}))
	defer webdav.Close()

	message, err := explainUsenetEngineRemoteConfigMismatch(context.Background(), config.UsenetEngineSettings{
		Type:          "decypharr",
		WebDAVBaseURL: webdav.URL,
		Category:      "mediastorm",
	})
	if err != nil {
		t.Fatalf("explainUsenetEngineRemoteConfigMismatch: %v", err)
	}
	if !strings.Contains(message, "custom folder") || !strings.Contains(message, "Category will still be sent") {
		t.Fatalf("message = %q", message)
	}
}

func TestInferAdminWebDAVPathPrefixFromRootFolder(t *testing.T) {
	webdav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/webdav/":
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/webdav/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/webdav/mediastorm/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:displayname>mediastorm</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`))
		case "/webdav/mediastorm/strmr-connection-test-1":
			w.WriteHeader(http.StatusMultiStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer webdav.Close()

	prefix, mappedURL, ok := inferAdminWebDAVPathPrefix(context.Background(), config.UsenetEngineSettings{
		Type:          "decypharr",
		WebDAVBaseURL: webdav.URL + "/webdav",
	}, "/mnt/debrid/decypharr_downloads/mediastorm/strmr-connection-test-1")
	if !ok {
		t.Fatal("expected prefix inference to succeed")
	}
	if prefix != "/mnt/debrid/decypharr_downloads" {
		t.Fatalf("prefix = %q, want /mnt/debrid/decypharr_downloads", prefix)
	}
	wantURL := webdav.URL + "/webdav/mediastorm/strmr-connection-test-1"
	if mappedURL != wantURL {
		t.Fatalf("mappedURL = %q, want %q", mappedURL, wantURL)
	}
}
