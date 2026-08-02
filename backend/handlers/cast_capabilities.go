package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"novastream/services/castcaps"
)

// CastCapabilitiesHandler exposes what is known about the Cast receivers on the
// LAN. Nothing here ever plays, loads, or launches anything on a receiver:
// every fact is either read passively over HTTP from the device itself, derived
// from a model/firmware prior, or graded from playback the user asked for.
type CastCapabilitiesHandler struct {
	store *castcaps.Store
}

func NewCastCapabilitiesHandler(cacheDir string) *CastCapabilitiesHandler {
	return &CastCapabilitiesHandler{store: castcaps.NewStore(cacheDir)}
}

// Store exposes the cache so session creation can consult it without any I/O.
func (h *CastCapabilitiesHandler) Store() *castcaps.Store {
	if h == nil {
		return nil
	}
	return h.store
}

// ListReceivers reports every receiver on the LAN with whatever is already
// cached about it. Discovery is a bounded port sweep and the capability lookup
// is cache-only, so this never blocks on a device.
func (h *CastCapabilitiesHandler) ListReceivers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	cidr := strings.TrimSpace(r.URL.Query().Get("cidr"))
	if cidr == "" {
		cidr = castcaps.LocalCIDR()
	}

	type receiverView struct {
		castcaps.Identity
		Capabilities *castcaps.Capabilities `json:"capabilities,omitempty"`
	}
	identities := castcaps.Discover(ctx, cidr)
	views := make([]receiverView, 0, len(identities))
	for _, identity := range identities {
		views = append(views, receiverView{Identity: identity, Capabilities: h.store.Lookup(identity.Host)})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"cidr": cidr, "receivers": views})
}

// DescribeReceiver returns the identity and current capability verdicts for one
// receiver. Store.Ensure reads eureka_info and the DIAL device description with
// plain GETs and applies the model prior; it never opens a Cast channel.
func (h *CastCapabilitiesHandler) DescribeReceiver(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		http.Error(w, "missing host parameter", http.StatusBadRequest)
		return
	}
	if net.ParseIP(host) == nil {
		http.Error(w, "host must be an IP address", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	caps, err := h.store.Ensure(ctx, host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(caps)
}
