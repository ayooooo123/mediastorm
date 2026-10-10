package landiscovery

import (
	"bytes"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
)

type fakeRegistration struct {
	mu       sync.Mutex
	text     []string
	shutdown int
}

func (f *fakeRegistration) SetText(text []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text = append([]string(nil), text...)
}

func (f *fakeRegistration) Shutdown() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdown++
}

func oneInterface() []net.Interface {
	return []net.Interface{{Index: 1, Name: "en0", Flags: net.FlagUp | net.FlagMulticast}}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func TestStartRegistersServiceWithTXTRecords(t *testing.T) {
	var gotInstance, gotService, gotDomain string
	var gotPort int
	var gotText []string
	reg := &fakeRegistration{}
	register := func(instance, service, domain string, port int, text []string, _ []net.Interface) (registration, error) {
		gotInstance, gotService, gotDomain, gotPort, gotText = instance, service, domain, port, text
		return reg, nil
	}

	adv := start(Config{Name: "Living Room", Port: 7777, Version: "1.2.3", BasePath: "/mediastorm/"}, register, oneInterface)
	if !adv.Active() {
		t.Fatal("advertiser should be active")
	}
	if gotInstance != "Living Room" || gotService != "_mediastorm._tcp" || gotDomain != "local." || gotPort != 7777 {
		t.Fatalf("registered %q %q %q %d", gotInstance, gotService, gotDomain, gotPort)
	}
	want := []string{"txtvers=1", "version=1.2.3", "name=Living Room", "apiPath=/mediastorm/api"}
	if strings.Join(gotText, "|") != strings.Join(want, "|") {
		t.Fatalf("txt = %v, want %v", gotText, want)
	}

	adv.SetServerID("ABCDEF")
	if joined := strings.Join(reg.text, "|"); !strings.Contains(joined, "serverId=abcdef") {
		t.Fatalf("serverId not published: %v", reg.text)
	}
	adv.Shutdown()
	adv.Shutdown()
	if reg.shutdown != 1 || adv.Active() {
		t.Fatalf("shutdown count = %d active=%v", reg.shutdown, adv.Active())
	}
}

func TestStartFailureIsNonFatalAndLogsOnce(t *testing.T) {
	logs := captureLog(t)
	calls := 0
	register := func(string, string, string, int, []string, []net.Interface) (registration, error) {
		calls++
		return nil, errors.New("no supported interface")
	}
	adv := start(Config{Port: 7777}, register, oneInterface)
	if adv == nil || adv.Active() {
		t.Fatal("failed registration must yield an inactive, non-nil advertiser")
	}
	// Every method stays safe on the inactive advertiser.
	adv.SetServerID("abc")
	adv.Shutdown()
	if calls != 1 {
		t.Fatalf("register called %d times", calls)
	}
	if lines := strings.Count(strings.TrimSpace(logs.String()), "\n") + 1; lines != 1 || !strings.Contains(logs.String(), "host networking") {
		t.Fatalf("expected exactly one explanatory log line, got %q", logs.String())
	}
}

func TestStartWithoutMulticastInterfaces(t *testing.T) {
	logs := captureLog(t)
	register := func(string, string, string, int, []string, []net.Interface) (registration, error) {
		t.Fatal("register must not be called without interfaces")
		return nil, nil
	}
	adv := start(Config{Port: 7777}, register, func() []net.Interface { return nil })
	if adv.Active() {
		t.Fatal("advertiser should be inactive")
	}
	if !strings.Contains(logs.String(), "no multicast-capable network interface") {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestStartRecoversFromPanics(t *testing.T) {
	captureLog(t)
	register := func(string, string, string, int, []string, []net.Interface) (registration, error) {
		panic("socket exploded")
	}
	adv := start(Config{Port: 7777}, register, oneInterface)
	if adv == nil || adv.Active() {
		t.Fatal("panic during registration must yield an inactive advertiser")
	}
}

func TestStartRejectsInvalidPort(t *testing.T) {
	captureLog(t)
	adv := start(Config{Port: 0}, func(string, string, string, int, []string, []net.Interface) (registration, error) {
		t.Fatal("register must not be called")
		return nil, nil
	}, oneInterface)
	if adv.Active() {
		t.Fatal("invalid port must not advertise")
	}
}

func TestNilAdvertiserIsSafe(t *testing.T) {
	var adv *Advertiser
	adv.SetServerID("x")
	adv.Shutdown()
	if adv.Active() {
		t.Fatal("nil advertiser is never active")
	}
}

func TestInstanceNameTruncatesToDNSLabel(t *testing.T) {
	long := strings.Repeat("é", 40) // 80 bytes
	got := instanceName(long)
	if len(got) > maxInstanceNameLen || !strings.HasPrefix(long, got) {
		t.Fatalf("instance name %q (%d bytes) not truncated on a rune boundary", got, len(got))
	}
}
