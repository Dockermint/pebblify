package api

import (
	"crypto/subtle"
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
func recoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("api panic recovered",
					"path", r.URL.Path,
					"method", r.Method,
					"panic", rec,
				)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
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
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkAuth returns true if r carries either an HTTP Basic password or a
// Bearer token matching expected. Both comparisons use constant-time
// primitives so an attacker cannot infer the token via timing.
func checkAuth(r *http.Request, expected string) bool {
	expectedBytes := []byte(expected)

	if _, password, ok := r.BasicAuth(); ok {
		if subtle.ConstantTimeCompare([]byte(password), expectedBytes) == 1 {
			return true
		}
	}

	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		token := strings.TrimPrefix(authz, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), expectedBytes) == 1 {
			return true
		}
	}
	return false
}
