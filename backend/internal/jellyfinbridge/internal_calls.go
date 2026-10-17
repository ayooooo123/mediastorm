package jellyfinbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/gorilla/mux"
)

// The bridge reuses mediastorm's HTTP handlers in-process. These helpers build
// a synthetic request carrying the caller's auth context, the way main-API
// middleware would have, and either capture the response or stream it
// straight through to the client.

// internalRequest builds a request for an in-process handler call.
func internalRequest(ctx context.Context, p *principal, method, target string, body []byte, vars map[string]string) *http.Request {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader).WithContext(ctx)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != nil {
		req = withPrincipal(req, p)
		if p.Client.DeviceID != "" {
			req.Header.Set("X-Client-ID", "jellyfin-"+p.Client.DeviceID)
		}
		req.Header.Set("User-Agent", "Jellyfin bridge ("+p.Client.Client+")")
	}
	if len(vars) > 0 {
		req = mux.SetURLVars(req, vars)
	}
	return req
}

// callJSON invokes h and decodes a JSON response into out.
func callJSON(h http.HandlerFunc, req *http.Request, out any) (int, error) {
	if h == nil {
		return http.StatusServiceUnavailable, fmt.Errorf("handler unavailable")
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code >= 400 {
		return rec.Code, fmt.Errorf("%s: %s", http.StatusText(rec.Code), bytes.TrimSpace(rec.Body.Bytes()))
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			return rec.Code, fmt.Errorf("decode %T: %w", out, err)
		}
	}
	return rec.Code, nil
}

// forward streams an in-process handler's response to the client, rewriting
// the request URL and route variables. Range and other client headers are kept.
func forward(w http.ResponseWriter, r *http.Request, p *principal, h http.HandlerFunc, target string, vars map[string]string) {
	if h == nil {
		writeError(w, http.StatusServiceUnavailable, "Not available")
		return
	}
	u, err := url.Parse(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bad internal URL")
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL = u
	r2.RequestURI = u.RequestURI()
	if p != nil {
		r2 = withPrincipal(r2, p)
		if p.Client.DeviceID != "" {
			r2.Header.Set("X-Client-ID", "jellyfin-"+p.Client.DeviceID)
		}
	}
	r2 = mux.SetURLVars(r2, vars)
	h(w, r2)
}
