package handlers

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"novastream/services/streaming"
)

// WebDAVHandler exposes cached Usenet streams over a simple byte-range capable endpoint.
type WebDAVHandler struct {
	streamer streaming.Provider
}

// NewWebDAVHandler returns a handler that proxies requests to the local stream provider.
func NewWebDAVHandler(provider streaming.Provider) *WebDAVHandler {
	return &WebDAVHandler{streamer: provider}
}

func (h *WebDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet, http.MethodHead:
		// Supported below
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.streamer == nil {
		http.Error(w, "stream provider not configured", http.StatusServiceUnavailable)
		return
	}

	cleanPath := strings.TrimPrefix(r.URL.Path, "/webdav")
	cleanPath = strings.TrimPrefix(cleanPath, "/")
	cleanPath = strings.TrimSpace(cleanPath)
	if cleanPath == "" {
		http.NotFound(w, r)
		return
	}

	rangeHeader := r.Header.Get("Range")
	log.Printf("[webdav] request path=%q method=%s range=%q", cleanPath, r.Method, rangeHeader)

	resp, err := h.streamer.Stream(r.Context(), streaming.Request{
		Path:        cleanPath,
		RangeHeader: rangeHeader,
		Method:      r.Method,
	})
	if err != nil {
		if errors.Is(err, streaming.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Close()

	// Propagate provider headers.
	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if w.Header().Get("Accept-Ranges") == "" {
		w.Header().Set("Accept-Ranges", "bytes")
	}

	status := resp.Status
	if status == 0 {
		if rangeHeader != "" {
			status = http.StatusPartialContent
		} else {
			status = http.StatusOK
		}
	}

	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}

	if resp.Body == nil {
		return
	}

	buf := make([]byte, 512*1024)
	flusher, _ := w.(http.Flusher)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				log.Printf("[webdav] write error path=%q err=%v", cleanPath, writeErr)
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("[webdav] read error path=%q err=%v", cleanPath, readErr)
			}
			break
		}
	}
}
