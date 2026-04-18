package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// statusRecorder wraps http.ResponseWriter so the access log middleware can
// capture the status code written by downstream handlers.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status before delegating to the underlying writer.
func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Write defers to the embedded writer; we rely on WriteHeader being called
// first for any non-200 status, per net/http conventions.
func (sr *statusRecorder) Write(b []byte) (int, error) {
	if sr.status == 0 {
		sr.status = http.StatusOK
	}
	return sr.ResponseWriter.Write(b)
}

// logRequests emits an access-log line at INFO for every served request. The
// recorded status defaults to 200 when the handler wrote a body without
// calling WriteHeader explicitly.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		logger.Info("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sr.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// recoverPanic converts handler panics into a 500 response and an ERROR log.
// The token and any other secret must never reach the log line; the recovered
// value is formatted with %v which avoids reflecting struct internals.
//
// A nil logger is tolerated: the middleware falls back to slog.Default at
// construction time so every downstream recover path sees a non-nil logger
// and cannot trigger a secondary nil-pointer panic.
func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("api panic recovered",
					"path", r.URL.Path,
					"method", r.Method,
					"panic", rec,
				)
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// basicAuth returns a middleware that enforces the configured authentication
// mode. When mode=basic_auth, the caller must present either a Basic auth
// header whose password matches token, or a Bearer token that matches token.
// Token comparison is constant-time. When mode=unsecure, the middleware is a
// pass-through; the caller is expected to log a startup warning once.
func basicAuth(token, mode string, next http.Handler) http.Handler {
	if mode == config.APIAuthUnsecure {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r, token) {
			w.Header().Set("WWW-Authenticate", `Basic realm="pebblify"`)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkAuth returns true if r carries either an HTTP Basic password or a
// Bearer token matching expected.
//
// Both comparisons hash the expected and provided credentials with SHA-256
// and then constant-time-compare the 32-byte digests. Hashing equalises the
// input length before the comparison so the timing surface does not leak the
// length of the secret; subtle.ConstantTimeCompare by itself leaks length
// because it short-circuits on unequal slice lengths.
//
// The Authorization header scheme is matched case-insensitively (RFC 7235
// §2.1) so clients emitting "bearer" or "BEARER" are accepted.
//
// An empty expected token always fails closed: allowing it would grant access
// to any unauthenticated request. Config validation already forbids an empty
// PEBBLIFY_BASIC_AUTH_TOKEN when mode=basic_auth, but this guard defends
// against any caller constructing the middleware directly.
func checkAuth(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	expectedDigest := sha256.Sum256([]byte(expected))

	if _, password, ok := r.BasicAuth(); ok && password != "" {
		providedDigest := sha256.Sum256([]byte(password))
		if subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1 {
			return true
		}
	}

	authz := r.Header.Get("Authorization")
	if scheme, token, ok := splitAuthzHeader(authz); ok &&
		strings.EqualFold(scheme, "Bearer") && token != "" {
		providedDigest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1 {
			return true
		}
	}
	return false
}

// splitAuthzHeader splits an Authorization header at the first whitespace run.
// The second return is the trimmed credentials; ok is false when the header
// is empty or carries no credentials component.
func splitAuthzHeader(h string) (scheme, creds string, ok bool) {
	trimmed := strings.TrimSpace(h)
	if trimmed == "" {
		return "", "", false
	}
	idx := strings.IndexAny(trimmed, " \t")
	if idx <= 0 {
		return "", "", false
	}
	scheme = trimmed[:idx]
	creds = strings.TrimSpace(trimmed[idx+1:])
	if creds == "" {
		return "", "", false
	}
	return scheme, creds, true
}

// writeJSONError writes a JSON error body with the given status. json.Marshal
// is used on a value holding the caller-supplied message so embedded quotes
// and control characters are safely escaped instead of corrupting the body.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	body, err := json.Marshal(errorResponse{Error: msg})
	if err != nil {
		// errorResponse has a single string field; marshaling cannot fail in
		// practice, but fall back to a pre-escaped literal if it somehow does.
		body = []byte(`{"error":"internal server error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Default().Error("api error write failed", "error", err)
	}
}
