package epg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"novastream/config"
	"novastream/models"
)

func (s *Service) downloadHDHomeRunGuide(ctx context.Context, discoveryURL string, schedule *models.EPGSchedule, source config.LivePlaylistSource) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()
	email, deviceIDs, err := config.NormalizeHDHomeRunGuideAuth(source.HDHomeRunGuideEmail, source.HDHomeRunGuideDeviceIDs)
	if err != nil {
		return err
	}
	client := *s.client
	client.Timeout = defaultHTTPTimeout
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return errors.New("HDHomeRun discovery redirect rejected")
	}
	baseGuideURL := hdHomeRunGuideURL
	if s.hdHomeRunGuideURL != "" {
		baseGuideURL = s.hdHomeRunGuideURL
	}
	if email != "" {
		// Account guide authentication has no dependency on tuner discovery.
		// A rejection remains visible; do not silently change methods.
		log.Printf("[hdhomerun-epg] guide attempt tuner=%q attempt=1 authMode=email-deviceids deviceCount=%d userAgent=%q", discoveryURL, len(strings.Split(deviceIDs, ",")), "MediaStorm")
		guideURL := baseGuideURL + "?" + url.Values{"Email": {email}, "DeviceIDs": {deviceIDs}}.Encode()
		importer := Service{client: &client, xmltvInvalidTimes: make(map[string]int), xmltvUserAgent: "MediaStorm"}
		err := importer.fetchXMLTVWithProxy(ctx, guideURL, "", schedule)
		logHDHomeRunGuideCoverage(discoveryURL, "download", schedule, importer.xmltvInvalidTimes, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("HDHomeRun account guide: %w", err)
		}
		return nil
	}
	previousAuth := ""
	for attempt := 1; attempt <= 2; attempt++ {
		// DeviceAuth rotates every 16-24 hours. Re-read it for each request,
		// including the one retry allowed after an upstream 403.
		deviceAuth, err := fetchHDHomeRunDeviceAuth(ctx, &client, discoveryURL)
		if err != nil {
			return err
		}
		log.Printf("[hdhomerun-epg] guide attempt tuner=%q attempt=%d authMode=deviceauth freshAuth=true authChanged=%v userAgent=%q", discoveryURL, attempt, attempt > 1 && deviceAuth != previousAuth, "MediaStorm")
		guideURL := baseGuideURL + "?" + url.Values{"DeviceAuth": {deviceAuth}}.Encode()
		// Identify our client without impersonating the official app. Never
		// send the rotating token through a configured proxy or log its URL.
		importer := Service{client: &client, xmltvInvalidTimes: make(map[string]int), xmltvUserAgent: "MediaStorm"}
		err = importer.fetchXMLTVWithProxy(ctx, guideURL, "", schedule)
		if err == nil {
			logHDHomeRunGuideCoverage(discoveryURL, "download", schedule, importer.xmltvInvalidTimes, time.Now().UTC())
			return nil
		}
		var statusErr *epgHTTPStatusError
		if attempt == 1 && errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusForbidden {
			log.Printf("[hdhomerun-epg] guide forbidden tuner=%q attempt=%d retry=fresh-discovery", discoveryURL, attempt)
			previousAuth = deviceAuth
			continue
		}
		logHDHomeRunGuideCoverage(discoveryURL, "download", schedule, importer.xmltvInvalidTimes, time.Now().UTC())
		return fmt.Errorf("HDHomeRun guide: %w", err)
	}
	return errors.New("HDHomeRun guide retry exhausted")
}

func fetchHDHomeRunDeviceAuth(ctx context.Context, client *http.Client, discoveryURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", errors.New("invalid HDHomeRun discovery URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("could not reach HDHomeRun tuner for DeviceAuth")
	}
	defer resp.Body.Close()
	log.Printf("[hdhomerun-epg] discovery tuner=%q status=%d", discoveryURL, resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HDHomeRun discovery returned HTTP %d", resp.StatusCode)
	}
	var device struct {
		DeviceAuth string `json:"DeviceAuth"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&device); err != nil || strings.TrimSpace(device.DeviceAuth) == "" {
		return "", errors.New("HDHomeRun discovery did not return a valid DeviceAuth")
	}
	return strings.TrimSpace(device.DeviceAuth), nil
}
