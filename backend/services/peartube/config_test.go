package peartube

import (
	"testing"

	"novastream/config"
)

// The whole point of the stored settings is that an operator on the admin page
// beats whatever the container's environment says, field by field.
func TestStoredSettingsWinOverTheEnvironment(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case EnabledEnv:
			return "1"
		case AutoSeedEnv:
			return "1"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{
		RelayURL: "http://from-settings:9000",
		Enabled:  new(true),
		AutoSeed: new(false),
	}, env)

	if resolved.RelayURL != "http://from-settings:9000" {
		t.Fatalf("relay URL = %q, want the stored one", resolved.RelayURL)
	}
	if !resolved.Enabled || resolved.AutoSeed {
		t.Fatalf("stored switches did not win: %+v", resolved)
	}
	if resolved.RelayURLFromEnv || resolved.EnabledFromEnv || resolved.AutoSeedFromEnv {
		t.Fatalf("stored values were reported as coming from the environment: %+v", resolved)
	}
}

// A stored "off" has to beat an environment "on", which is the case that forces
// the switches to be pointers rather than plain bools.
func TestStoredDisableBeatsAnEnabledEnvironment(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case EnabledEnv:
			return "true"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{Enabled: new(false)}, env)
	if resolved.Enabled || resolved.RelayURL != "" {
		t.Fatalf("stored disable was overridden: %+v", resolved)
	}
}

// While a field is absent the environment is its default, and the UI has to be
// able to say so rather than presenting it as the operator's own choice.
func TestAbsentFieldsFallBackToTheEnvironmentAndSaySo(t *testing.T) {
	env := func(key string) string {
		switch key {
		case RelayURLEnv:
			return "http://from-env:8178"
		case AutoSeedEnv:
			return "off"
		}
		return ""
	}

	resolved := resolve(config.PearTubeSettings{}, env)
	if resolved.RelayURL != "http://from-env:8178" || !resolved.RelayURLFromEnv {
		t.Fatalf("relay URL did not come from the environment: %+v", resolved)
	}
	if !resolved.Enabled || resolved.EnabledFromEnv {
		t.Fatalf("a configured URL should enable without the environment saying so: %+v", resolved)
	}
	if resolved.AutoSeed || !resolved.AutoSeedFromEnv {
		t.Fatalf("autoseed did not come from the environment: %+v", resolved)
	}
}

// An empty relay URL with nothing in the environment is the shipped default and
// must leave the integration completely absent.
func TestEmptyRelayURLLeavesTheIntegrationOff(t *testing.T) {
	none := func(string) string { return "" }

	resolved := resolve(config.PearTubeSettings{RelayURL: "   "}, none)
	if resolved.Enabled || resolved.RelayURL != "" {
		t.Fatalf("an empty relay URL left the integration on: %+v", resolved)
	}
	// Autoseed still resolves to its default; the nil client is the outer gate.
	if !resolved.AutoSeed {
		t.Fatalf("autoseed default changed: %+v", resolved)
	}
}

// Storing a relay URL and nothing else is the ordinary way to turn this on from
// the admin page, so the URL alone must be the switch.
func TestStoredRelayURLAloneEnablesTheIntegration(t *testing.T) {
	resolved := resolve(config.PearTubeSettings{RelayURL: "http://relay.internal:8178/"}, func(string) string { return "" })
	if !resolved.Enabled || resolved.RelayURL != "http://relay.internal:8178/" {
		t.Fatalf("stored URL did not enable the integration: %+v", resolved)
	}
	if !resolved.AutoSeed {
		t.Fatalf("autoseed should default on once a relay is configured: %+v", resolved)
	}
}

// Configure replaces the process-wide client, which is what makes a settings
// save take effect without restarting the container.
func TestConfigureReplacesTheProcessWideClient(t *testing.T) {
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultClient = nil
		defaultConfigured = false
		defaultMu.Unlock()
	})

	if client := Configure(Resolved{}); client != nil {
		t.Fatalf("empty configuration produced a client: %v", client)
	}
	if Default() != nil {
		t.Fatal("Default returned a client after an empty configuration")
	}

	client := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:8178"})
	if client == nil || client.BaseURL() != "http://relay.internal:8178" {
		t.Fatalf("client = %v", client)
	}
	if Default() != client {
		t.Fatal("Default did not return the configured client")
	}

	// An unchanged URL must keep the same client, so a save unrelated to p2p
	// does not discard the catalog cache.
	if again := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:8178"}); again != client {
		t.Fatal("an unchanged relay URL rebuilt the client")
	}

	moved := Configure(Resolved{Enabled: true, RelayURL: "http://relay.internal:9000"})
	if moved == client || moved.BaseURL() != "http://relay.internal:9000" {
		t.Fatalf("moved client = %v", moved)
	}

	if Configure(Resolved{}) != nil || Default() != nil {
		t.Fatal("clearing the relay URL left a client installed")
	}
}
