package peartube

import (
	"os"
	"strings"

	"novastream/config"
)

// Resolved is the effective PearTube configuration: what the integration will
// actually do, after the admin settings and the environment have been combined.
//
// It also records which fields the environment supplied. The admin UI shows
// that, because a screen that renders an environment-provided value as if the
// operator had chosen it is a screen that lies about why the server behaves the
// way it does.
type Resolved struct {
	// RelayURL is the relay to talk to. Empty means the integration does not
	// exist for this install: no relay, no p2p search source, no seeding.
	RelayURL string
	// Enabled is whether a relay should be used at all. When false RelayURL is
	// cleared, so callers only have to check the URL.
	Enabled bool
	// AutoSeed is whether starting a playback publishes it into the swarm. It
	// says nothing about whether a relay exists; RelayURL is the outer gate.
	AutoSeed bool

	RelayURLFromEnv bool
	EnabledFromEnv  bool
	AutoSeedFromEnv bool
}

// Resolve combines the operator's stored settings with the environment.
//
// See config.PearTubeSettings for the precedence rule this implements: a stored
// value wins when present, and the environment variable is the default while it
// is absent. The zero settings value therefore behaves exactly as the
// environment-only integration did.
func Resolve(stored config.PearTubeSettings) Resolved {
	return resolve(stored, os.Getenv)
}

func resolve(stored config.PearTubeSettings, getenv func(string) string) Resolved {
	var resolved Resolved

	resolved.RelayURL = strings.TrimSpace(stored.RelayURL)
	if resolved.RelayURL == "" {
		if fromEnv := strings.TrimSpace(getenv(RelayURLEnv)); fromEnv != "" {
			resolved.RelayURL = fromEnv
			resolved.RelayURLFromEnv = true
		}
	}

	switch {
	case stored.Enabled != nil:
		resolved.Enabled = *stored.Enabled
	default:
		if value, ok := parseSwitch(getenv(EnabledEnv)); ok {
			resolved.Enabled = value
			resolved.EnabledFromEnv = true
		} else {
			// Unset: a configured URL is the switch, which is what keeps an
			// install that never asked for p2p inert.
			resolved.Enabled = resolved.RelayURL != ""
		}
	}
	switch {
	case !resolved.Enabled:
		// One field to check downstream. An explicit disable beats a URL.
		resolved.RelayURL = ""
	case resolved.RelayURL == "":
		// Enabled with no URL anywhere means the relay is where `peartube
		// relay` listens out of the box.
		resolved.RelayURL = DefaultRelayURL
	}

	switch {
	case stored.AutoSeed != nil:
		resolved.AutoSeed = *stored.AutoSeed
	default:
		if value, ok := parseSwitch(getenv(AutoSeedEnv)); ok {
			resolved.AutoSeed = value
			resolved.AutoSeedFromEnv = true
		} else {
			resolved.AutoSeed = true
		}
	}

	return resolved
}

// parseSwitch reads an operator's boolean environment value, reporting whether
// it said anything at all. Unset and unrecognized are the same answer — no
// opinion — so the caller falls back to its own default instead of reading a
// typo as "off".
func parseSwitch(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}
