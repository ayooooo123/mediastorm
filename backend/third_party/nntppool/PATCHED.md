# Vendored nntppool v2.3.2 (patched)

Copy of `github.com/javi11/nntppool/v2@v2.3.2` (tests and fixtures removed),
wired in via the `replace` directive in `backend/go.mod`.

Patch: `pkg/nntpcli/connection.go` `Ping()` expects `111` for `DATE`
(RFC 3977 §7.1). Upstream expects `200`, so the pool's provider health check
fails on every compliant server and marks all providers offline every cycle.

Drop this directory and the `replace` once upstream ships a fix.
