// Package demomedia makes a folder of royalty-free video files the only
// playable sources of a demo-mode server (`-demo`), so it can be handed to
// reviewers without any indexer or debrid service configured.
//
// Files are matched by IMDb ID: "<imdbID>.<ext>" for a movie and
// "<imdbID>.S01E02.<ext>" for an episode, e.g. "tt0063350.mp4".
package demomedia

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"novastream/models"
	"novastream/services/streaming"
)

const streamPathPrefix = "demo:"

var (
	fileNamePattern = regexp.MustCompile(`(?i)^(tt\d+)(?:\.S(\d{1,2})E(\d{1,3}))?\.(mp4|m4v|mkv|mov|webm|ts)$`)
	queryEpisode    = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`)
)

// Catalog lists and serves the files in one directory.
type Catalog struct {
	dir string
}

func NewCatalog(dir string) *Catalog {
	return &Catalog{dir: dir}
}

type entry struct {
	name            string
	imdbID          string
	season, episode int
	size            int64
	modified        time.Time
}

func (c *Catalog) entries() []entry {
	files, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	var out []entry
	for _, f := range files {
		m := fileNamePattern.FindStringSubmatch(f.Name())
		if f.IsDir() || m == nil {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		e := entry{name: f.Name(), imdbID: strings.ToLower(m[1]), size: info.Size(), modified: info.ModTime()}
		e.season, _ = strconv.Atoi(m[2])
		e.episode, _ = strconv.Atoi(m[3])
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// Results returns the catalog files for a title as search results. An episode
// search ("... S01E02" in the query) matches only that episode's file.
func (c *Catalog) Results(imdbID, query, mediaType string) []models.NZBResult {
	imdbID = strings.ToLower(strings.TrimSpace(imdbID))
	if c == nil || imdbID == "" {
		return []models.NZBResult{}
	}
	season, episode := 0, 0
	if m := queryEpisode.FindStringSubmatch(query); m != nil {
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
	}
	results := []models.NZBResult{}
	for _, e := range c.entries() {
		if e.imdbID != imdbID {
			continue
		}
		if mediaType == "series" && (e.season != season || e.episode != episode) {
			continue
		}
		if mediaType != "series" && e.season != 0 {
			continue
		}
		title := strings.TrimSpace(query)
		if title == "" {
			title = e.name
		}
		results = append(results, models.NZBResult{
			Title:       title,
			Indexer:     "Demo",
			GUID:        streamPathPrefix + e.name,
			Link:        streamPathPrefix + e.name,
			DownloadURL: streamPathPrefix + e.name,
			SizeBytes:   e.size,
			PublishDate: e.modified,
			ServiceType: models.ServiceTypeUsenet,
			Attributes:  map[string]string{"resolution": "SD"},
		})
	}
	return results
}

// IsStreamPath reports whether a search result link or stream path is a catalog file.
func IsStreamPath(path string) bool {
	return strings.HasPrefix(strings.TrimSpace(path), streamPathPrefix)
}

// Resolution is the playback resolution of a catalog result: its link is already the stream path.
func (c *Catalog) Resolution(link string) (*models.PlaybackResolution, error) {
	path, err := c.file(link)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, streaming.ErrNotFound
	}
	return &models.PlaybackResolution{WebDAVPath: strings.TrimSpace(link), HealthStatus: "healthy", FileSize: info.Size()}, nil
}

// file maps "demo:<name>" to a file in the catalog directory, refusing anything else.
func (c *Catalog) file(streamPath string) (string, error) {
	if c == nil || !IsStreamPath(streamPath) {
		return "", streaming.ErrNotFound
	}
	name := strings.TrimPrefix(strings.TrimSpace(streamPath), streamPathPrefix)
	if name != filepath.Base(name) || !fileNamePattern.MatchString(name) {
		return "", streaming.ErrNotFound
	}
	return filepath.Join(c.dir, name), nil
}

// GetDirectURL lets FFmpeg read the file directly (streaming.DirectURLProvider).
func (c *Catalog) GetDirectURL(_ context.Context, streamPath string) (string, error) {
	path, err := c.file(streamPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", streaming.ErrNotFound
	}
	return path, nil
}

// Stream serves a catalog file with single-range support (streaming.Provider).
func (c *Catalog) Stream(_ context.Context, req streaming.Request) (*streaming.Response, error) {
	path, err := c.file(req.Path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, streaming.ErrNotFound
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		_ = file.Close()
		return nil, streaming.ErrNotFound
	}
	size, name := info.Size(), filepath.Base(path)
	headers := make(http.Header)
	headers.Set("Accept-Ranges", "bytes")
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); contentType != "" {
		headers.Set("Content-Type", contentType)
	}

	start, end := int64(0), size-1
	status := http.StatusOK
	if req.RangeHeader != "" {
		var ok bool
		if start, end, ok = parseRange(req.RangeHeader, size); !ok {
			_ = file.Close()
			headers.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			return &streaming.Response{Headers: headers, Status: http.StatusRequestedRangeNotSatisfiable, Filename: name}, nil
		}
		status = http.StatusPartialContent
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	length := end - start + 1
	headers.Set("Content-Length", strconv.FormatInt(length, 10))
	if strings.EqualFold(req.Method, http.MethodHead) {
		_ = file.Close()
		return &streaming.Response{Headers: headers, Status: status, ContentLength: length, Filename: name}, nil
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	body := struct {
		io.Reader
		io.Closer
	}{io.LimitReader(file, length), file}
	return &streaming.Response{Body: body, Headers: headers, Status: status, ContentLength: length, Filename: name}, nil
}

// parseRange parses a single "bytes=" range, including open-ended and suffix forms.
func parseRange(header string, size int64) (int64, int64, bool) {
	spec, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(header)), "bytes=")
	first, last, dash := strings.Cut(spec, "-")
	if !found || !dash || strings.Contains(spec, ",") || size <= 0 {
		return 0, 0, false
	}
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return max(size-n, 0), size - 1, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if last != "" {
		if end, err = strconv.ParseInt(last, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		end = min(end, size-1)
	}
	return start, end, true
}
