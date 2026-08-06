package castcaps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// receiverStub serves recorded payloads on the two paths Describe is allowed to
// read, and records every path asked for so a test can prove nothing else was.
type receiverStub struct {
	eureka   []byte
	eurekaSt int
	dial     []byte
	dialSt   int

	asked []string
}

func (s *receiverStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.asked = append(s.asked, r.URL.RequestURI())
		switch r.URL.Path {
		case "/setup/eureka_info":
			if s.eurekaSt != 0 {
				w.WriteHeader(s.eurekaSt)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(s.eureka)
		case "/ssdp/device-desc.xml":
			if s.dialSt != 0 {
				w.WriteHeader(s.dialSt)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(s.dial)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDescribe(t *testing.T) {
	lg := fixture(t, "eureka_info_lg_c2.json")
	dongle := fixture(t, "eureka_info_dongle.json")
	dialDongle := fixture(t, "device_desc_dongle.xml")

	tests := []struct {
		name    string
		host    string
		stub    receiverStub
		want    Identity
		wantErr bool
	}{
		{
			// 192.168.8.105: modern webOS panel. Serves eureka_info, 404s DIAL.
			name: "lg panel without DIAL document",
			host: "192.168.8.105",
			stub: receiverStub{eureka: lg, dialSt: http.StatusNotFound},
			want: Identity{
				Host:          "192.168.8.105",
				Name:          "[LG] webOS TV OLED65C2PUA",
				BuildRevision: "1.68.cast_20250829_0200_RC13.800768591",
				UDN:           "1b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9",
			},
		},
		{
			// 192.168.8.108: legacy dongle. Serves both; only DIAL names the model.
			name: "legacy dongle serving both documents",
			host: "192.168.8.108",
			stub: receiverStub{eureka: dongle, dial: dialDongle},
			want: Identity{
				Host:          "192.168.8.108",
				Name:          "Avery's room TV",
				ModelName:     "Eureka Dongle",
				Manufacturer:  "Google Inc.",
				BuildRevision: "1.56.291998",
				UDN:           "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a",
			},
		},
		{
			name: "DIAL only still identifies the model",
			host: "10.0.0.5",
			stub: receiverStub{eurekaSt: http.StatusServiceUnavailable, dial: dialDongle},
			want: Identity{
				Host:         "10.0.0.5",
				Name:         "Avery's room TV",
				ModelName:    "Eureka Dongle",
				Manufacturer: "Google Inc.",
			},
		},
		{
			name: "unparseable eureka payload falls back to DIAL",
			host: "10.0.0.6",
			stub: receiverStub{eureka: []byte("<html>not json</html>"), dial: dialDongle},
			want: Identity{
				Host:         "10.0.0.6",
				Name:         "Avery's room TV",
				ModelName:    "Eureka Dongle",
				Manufacturer: "Google Inc.",
			},
		},
		{
			name: "nameless receiver falls back to its address",
			host: "10.0.0.7",
			stub: receiverStub{eureka: []byte(`{"cast_build_revision":"1.42.1"}`), dialSt: http.StatusNotFound},
			want: Identity{Host: "10.0.0.7", Name: "10.0.0.7", BuildRevision: "1.42.1"},
		},
		{
			name:    "no source answers",
			host:    "10.0.0.8",
			stub:    receiverStub{eurekaSt: http.StatusNotFound, dialSt: http.StatusNotFound},
			want:    Identity{Host: "10.0.0.8"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := tc.stub
			server := stub.serve(t)

			got, err := describe(context.Background(), server.Client(), server.URL, tc.host)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("identity = %#v, want %#v", got, tc.want)
			}

			asked := append([]string(nil), stub.asked...)
			sort.Strings(asked)
			want := []string{"/setup/eureka_info?options=detail", "/ssdp/device-desc.xml"}
			if !reflect.DeepEqual(asked, want) {
				t.Fatalf("requested %v, want exactly %v: Describe must stay passive", asked, want)
			}
		})
	}
}

func TestDescribe_UnreachableHost(t *testing.T) {
	// Port 1 on the loopback refuses instantly, so this exercises the transport
	// failure path without waiting out a timeout.
	if _, err := describe(context.Background(), describeClient(), "http://127.0.0.1:1", "127.0.0.1"); err == nil {
		t.Fatal("expected an error when nothing answers")
	}
}

func TestSetupPortIsPassive(t *testing.T) {
	// 8009 is the CASTV2 port: reaching it is how a caller would launch an app
	// and load media, which this package must never do.
	if setupPort != 8008 {
		t.Fatalf("setupPort = %d, want 8008", setupPort)
	}
}
