package api

import (
	"crypto/hmac"
	"net/http"

	"github.com/hiren223344/frpay/internal/ratelimit"
)

// apiKeyMiddleware guards POST /v1/orders with a single shared secret —
// this is a single-tenant service, not a per-caller credential system.
// The comparison is constant-time to avoid leaking the key via timing.
func apiKeyMiddleware(expectedKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get("X-API-Key")
			if provided == "" || !hmac.Equal([]byte(provided), []byte(expectedKey)) {
				writeError(w, http.StatusUnauthorized, "invalid or missing X-API-Key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsPublicGET allows any origin to read this route. Only applied to
// the unauthenticated, secret-free GET endpoints a frontend polls
// directly — never to the API-key-guarded order creation route.
// A plain cross-origin GET with no custom headers is a CORS "simple
// request", so no preflight OPTIONS request needs handling here.
func corsPublicGET(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware rejects requests once the caller (identified by
// keyFunc) exceeds the configured rate.
func rateLimitMiddleware(limiter *ratelimit.Limiter, keyFunc func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow(r.Context(), keyFunc(r)) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
