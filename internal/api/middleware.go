package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/hiren223344/frpay/internal/merchant"
	"github.com/hiren223344/frpay/internal/ratelimit"
)

const maxTimestampSkew = 5 * time.Minute

// authMiddleware enforces API key + HMAC request signing. A merchant
// signs every request with: X-Frenix-Key (their API key), X-Frenix-
// Timestamp (unix seconds) and X-Frenix-Signature (see merchant.Sign).
// The timestamp is bound into the signature and checked against a skew
// window so a captured request can't be replayed indefinitely.
func authMiddleware(svc *merchant.Service, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-Frenix-Key")
			signature := r.Header.Get("X-Frenix-Signature")
			timestampHeader := r.Header.Get("X-Frenix-Timestamp")
			if apiKey == "" || signature == "" || timestampHeader == "" {
				writeError(w, http.StatusUnauthorized, "missing authentication headers")
				return
			}

			timestamp, err := strconv.ParseInt(timestampHeader, 10, 64)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid timestamp")
				return
			}
			skew := time.Since(time.Unix(timestamp, 0))
			if skew < 0 {
				skew = -skew
			}
			if skew > maxTimestampSkew {
				writeError(w, http.StatusUnauthorized, "request timestamp outside allowed window")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			if err != nil {
				writeError(w, http.StatusBadRequest, "failed to read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			m, secret, err := svc.Authenticate(r.Context(), apiKey)
			if err != nil {
				if !errors.Is(err, merchant.ErrNotFound) {
					logger.Error("merchant authentication error", "error", err)
				}
				writeError(w, http.StatusUnauthorized, "invalid API key")
				return
			}

			if !merchant.Verify(secret, r.Method, r.URL.Path, timestamp, body, signature) {
				writeError(w, http.StatusUnauthorized, "invalid signature")
				return
			}

			r = r.WithContext(withMerchant(r.Context(), m))
			next.ServeHTTP(w, r)
		})
	}
}

// corsPublicGET allows any origin to read this route. Only applied to
// the two unauthenticated, secret-free GET endpoints a checkout page's
// browser JS polls directly (see router.go) — never to anything
// merchant-authenticated. A plain cross-origin GET with no custom
// headers is a CORS "simple request", so no preflight OPTIONS request
// needs handling here.
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
