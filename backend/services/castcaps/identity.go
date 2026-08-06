package castcaps

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// setupPort is the plain-HTTP port every Cast receiver serves its setup and DIAL
// documents on. It is the only port this package ever talks to; 8009 (CASTV2) is
// what would let us launch an app, and we never do.
const setupPort = 8008

// describeTimeout bounds each passive GET. Receivers on the LAN answer in
// milliseconds; anything slower is asleep or not a Cast device.
const describeTimeout = 3500 * time.Millisecond

// maxDescribeBody caps what we read from an unauthenticated device on the LAN.
const maxDescribeBody = 256 << 10

// Identity is what a receiver tells us without being asked to play anything.
type Identity struct {
	Host          string `json:"host"`
	Name          string `json:"name"`          // eureka_info.name
	ModelName     string `json:"modelName"`     // DIAL <modelName>, "" when unavailable
	Manufacturer  string `json:"manufacturer"`  // DIAL <manufacturer>
	BuildRevision string `json:"buildRevision"` // eureka_info.cast_build_revision
	UDN           string `json:"udn"`           // eureka_info.ssdp_udn
}

// eurekaInfo is the subset of /setup/eureka_info worth keeping.
type eurekaInfo struct {
	Name              string `json:"name"`
	SSDPUDN           string `json:"ssdp_udn"`
	CastBuildRevision string `json:"cast_build_revision"`
	BuildVersion      string `json:"build_version"`
	Detail            struct {
		ModelName    string `json:"model_name"`
		Manufacturer string `json:"manufacturer"`
	} `json:"detail"`
	Settings struct {
		Name string `json:"name"`
	} `json:"settings"`
}

// dialDeviceDesc is the subset of the DIAL/UPnP device description worth keeping.
// LG's webOS Cast implementation does not serve this document at all, so every
// field here is optional.
type dialDeviceDesc struct {
	XMLName xml.Name `xml:"root"`
	Device  struct {
		FriendlyName string `xml:"friendlyName"`
		Manufacturer string `xml:"manufacturer"`
		ModelName    string `xml:"modelName"`
	} `xml:"device"`
}

// Describe reads a receiver's identity with two passive GETs on port 8008. It
// never launches an app and never loads media.
//
// The two sources disagree about which devices serve them: a Chromecast dongle
// answers both, an LG panel answers eureka_info and 404s the DIAL description.
// Either one alone is enough, so only a receiver that answered nothing is an
// error.
func Describe(ctx context.Context, host string) (Identity, error) {
	return describe(ctx, describeClient(), fmt.Sprintf("http://%s:%d", host, setupPort), host)
}

func describeClient() *http.Client {
	return &http.Client{Timeout: describeTimeout}
}

// describe is Describe with the base URL injected so tests can serve recorded
// payloads instead of touching a receiver somebody is watching.
func describe(ctx context.Context, client *http.Client, base, host string) (Identity, error) {
	identity := Identity{Host: host}

	var eurekaErr, dialErr error
	if info, err := fetchEurekaInfo(ctx, client, base); err != nil {
		eurekaErr = err
	} else {
		identity.Name = firstNonEmpty(info.Name, info.Settings.Name)
		identity.BuildRevision = firstNonEmpty(info.CastBuildRevision, info.BuildVersion)
		identity.UDN = info.SSDPUDN
		// eureka_info's detail block is a weaker source than DIAL for these two,
		// so DIAL overwrites them below when it answers.
		identity.ModelName = info.Detail.ModelName
		identity.Manufacturer = info.Detail.Manufacturer
	}

	if desc, err := fetchDeviceDesc(ctx, client, base); err != nil {
		dialErr = err
	} else {
		if name := strings.TrimSpace(desc.Device.ModelName); name != "" {
			identity.ModelName = name
		}
		if maker := strings.TrimSpace(desc.Device.Manufacturer); maker != "" {
			identity.Manufacturer = maker
		}
		identity.Name = firstNonEmpty(identity.Name, desc.Device.FriendlyName)
	}

	if eurekaErr != nil && dialErr != nil {
		return Identity{Host: host}, fmt.Errorf("describe %s: eureka_info: %w; device-desc: %v", host, eurekaErr, dialErr)
	}
	if identity.Name == "" {
		identity.Name = host
	}
	return identity, nil
}

func fetchEurekaInfo(ctx context.Context, client *http.Client, base string) (eurekaInfo, error) {
	var info eurekaInfo
	body, err := getBody(ctx, client, base+"/setup/eureka_info?options=detail")
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return info, fmt.Errorf("decode eureka_info: %w", err)
	}
	return info, nil
}

func fetchDeviceDesc(ctx context.Context, client *http.Client, base string) (dialDeviceDesc, error) {
	var desc dialDeviceDesc
	body, err := getBody(ctx, client, base+"/ssdp/device-desc.xml")
	if err != nil {
		return desc, err
	}
	if err := xml.Unmarshal(body, &desc); err != nil {
		return desc, fmt.Errorf("decode device-desc.xml: %w", err)
	}
	return desc, nil
}

func getBody(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDescribeBody))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("empty response body")
	}
	return body, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
