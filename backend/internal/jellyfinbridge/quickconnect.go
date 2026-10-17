package jellyfinbridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
	"novastream/models"
)

// Quick Connect lets a device without a keyboard (a TV) sign in: it shows a
// six-digit code, a user who is already signed in elsewhere approves the
// code, and the device then exchanges its secret for a token. Same flow and
// timings as Jellyfin's QuickConnectManager.
const (
	quickConnectTTL        = 10 * time.Minute
	quickConnectMaxPending = 64
	quickConnectMaxPerIP   = 5
)

type quickConnectRequest struct {
	Secret  string
	Code    string
	Client  clientInfo
	IP      string
	Created time.Time
	Expires time.Time
	// Set by Authorize: the account and profile the device signs in as. The
	// token is only issued when the device collects it, so an approval that
	// is never collected leaves no session behind.
	AccountID string
	ProfileID string
}

func (q *quickConnectRequest) authorized() bool { return q.ProfileID != "" }

type quickConnect struct {
	mu       sync.Mutex
	bySecret map[string]*quickConnectRequest
}

func newQuickConnect() *quickConnect {
	return &quickConnect{bySecret: map[string]*quickConnectRequest{}}
}

func (qc *quickConnect) expireLocked(now time.Time) {
	for secret, req := range qc.bySecret {
		if now.After(req.Expires) {
			delete(qc.bySecret, secret)
		}
	}
}

func (qc *quickConnect) initiate(client clientInfo, ip string) (*quickConnectRequest, error) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	now := time.Now()
	qc.expireLocked(now)
	perIP := 0
	codes := map[string]bool{}
	for _, req := range qc.bySecret {
		codes[req.Code] = true
		if req.IP == ip {
			perIP++
		}
	}
	if len(qc.bySecret) >= quickConnectMaxPending || perIP >= quickConnectMaxPerIP {
		return nil, fmt.Errorf("too many pending quick connect requests")
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, err
	}
	var code string
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(900000))
		if err != nil {
			return nil, err
		}
		if code = fmt.Sprintf("%06d", n.Int64()+100000); !codes[code] {
			break
		}
	}
	req := &quickConnectRequest{
		Secret: hex.EncodeToString(secretBytes), Code: code, Client: client, IP: ip,
		Created: now, Expires: now.Add(quickConnectTTL),
	}
	qc.bySecret[req.Secret] = req
	copied := *req
	return &copied, nil
}

func (qc *quickConnect) get(secret string) (quickConnectRequest, bool) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.expireLocked(time.Now())
	req, ok := qc.bySecret[secret]
	if !ok {
		return quickConnectRequest{}, false
	}
	return *req, true
}

// authorize approves the pending request with code for a profile.
func (qc *quickConnect) authorize(code, accountID, profileID string) error {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	now := time.Now()
	qc.expireLocked(now)
	for _, req := range qc.bySecret {
		if req.Code != code {
			continue
		}
		if req.authorized() {
			return fmt.Errorf("request is already authorized")
		}
		req.AccountID, req.ProfileID = accountID, profileID
		req.Expires = now.Add(quickConnectTTL)
		return nil
	}
	return errQuickConnectUnknown
}

// collect hands out an authorized request once.
func (qc *quickConnect) collect(secret string) (quickConnectRequest, bool) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.expireLocked(time.Now())
	req, ok := qc.bySecret[secret]
	if !ok || !req.authorized() {
		return quickConnectRequest{}, false
	}
	delete(qc.bySecret, secret)
	return *req, true
}

var errQuickConnectUnknown = fmt.Errorf("unable to find request")

func quickConnectResult(req quickConnectRequest) dto.QuickConnectResult {
	return dto.QuickConnectResult{
		Authenticated: req.authorized(),
		Secret:        req.Secret,
		Code:          req.Code,
		DeviceID:      req.Client.DeviceID,
		DeviceName:    req.Client.Device,
		AppName:       req.Client.Client,
		AppVersion:    req.Client.Version,
		DateAdded:     req.Created.UTC(),
	}
}

func (s *Server) handleQuickConnectEnabled(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settings().QuickConnect)
}

// handleQuickConnectInitiate starts a request for the calling device.
func (s *Server) handleQuickConnectInitiate(w http.ResponseWriter, r *http.Request) {
	if !s.settings().QuickConnect {
		writeError(w, http.StatusUnauthorized, "Quick connect is disabled")
		return
	}
	info := requestClientInfo(r)
	if info.DeviceID == "" || info.Device == "" || info.Client == "" || info.Version == "" {
		writeError(w, http.StatusBadRequest, "Client, Device, DeviceId and Version are required")
		return
	}
	req, err := s.qc.initiate(info, ipLimitKey(s.clientIP(r)))
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "Too many pending requests. Try again later.")
		return
	}
	writeJSON(w, http.StatusOK, quickConnectResult(*req))
}

// handleQuickConnectConnect is the device's status poll.
func (s *Server) handleQuickConnectConnect(w http.ResponseWriter, r *http.Request) {
	if !s.settings().QuickConnect {
		writeError(w, http.StatusUnauthorized, "Quick connect is disabled")
		return
	}
	req, ok := s.qc.get(queryValue(r, "secret"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unable to find request with provided secret")
		return
	}
	writeJSON(w, http.StatusOK, quickConnectResult(req))
}

// handleQuickConnectAuthorize approves a code for the signed-in profile.
// Like Jellyfin for non-administrators, a user can only sign devices in as
// themselves.
func (s *Server) handleQuickConnectAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.settings().QuickConnect {
		writeError(w, http.StatusUnauthorized, "Quick connect is disabled")
		return
	}
	p := principalFrom(r)
	if uid := normalizeID(queryValue(r, "userId")); uid != "" && uid != s.userID(p.Profile) {
		writeError(w, http.StatusForbidden, "Quick connect can only sign devices in as yourself")
		return
	}
	code := strings.TrimSpace(queryValue(r, "code"))
	if err := s.qc.authorize(code, p.Session.AccountID, p.Profile.ID); err != nil {
		if err == errQuickConnectUnknown {
			writeError(w, http.StatusNotFound, "Unable to find request")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	log.Printf("[jellyfin-bridge] quick connect code approved for profile %q", p.Profile.Name)
	writeJSON(w, http.StatusOK, true)
}

// handleAuthenticateWithQuickConnect exchanges an approved secret for a token.
func (s *Server) handleAuthenticateWithQuickConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"Secret"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body)
	req, ok := s.qc.collect(strings.TrimSpace(body.Secret))
	if !s.settings().QuickConnect || !ok {
		writeError(w, http.StatusNotFound, "Unable to find request")
		return
	}
	// Re-check the approval: the profile may have been removed or the
	// account expired since.
	profile, found := s.deps.Users.Get(req.ProfileID)
	account, accountFound := s.deps.Accounts.Get(req.AccountID)
	if !found || !accountFound || !s.deps.Users.BelongsToAccount(profile.ID, account.ID) || s.deps.Accounts.IsExpired(account.ID) {
		writeError(w, http.StatusUnauthorized, "Error processing request.")
		return
	}
	userAgent := strings.TrimSpace(strings.Join([]string{req.Client.Client, req.Client.Version, req.Client.Device}, " "))
	session, err := s.deps.Sessions.CreateScopedWithResource(account.ID, account.IsMaster, "Jellyfin: "+userAgent, s.clientIP(r), bridgeTokenTTL, models.SessionScopeJellyfin, profile.ID)
	if err != nil {
		log.Printf("[jellyfin-bridge] create session: %v", err)
		writeError(w, http.StatusInternalServerError, "Could not create session")
		return
	}
	log.Printf("[jellyfin-bridge] %q signed in as profile %q via quick connect (client %s %s)", account.Username, profile.Name, req.Client.Client, req.Client.Device)
	writeJSON(w, http.StatusOK, dto.AuthenticationResult{
		User:        s.userDto(profile),
		AccessToken: session.Token,
		ServerID:    s.serverID,
	})
}
