package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/orders"
	"github.com/hiren223344/frpay/internal/ratelimit"
)

func NewRouter(ordersSvc *orders.Service, chainMgr *chains.Manager, addresses map[chains.Chain]string, apiKey string, ordersLimiter, publicLimiter *ratelimit.Limiter, logger *slog.Logger) http.Handler {
	h := NewHandlers(ordersSvc, chainMgr, addresses, logger)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(slogRequestLogger(logger))

	r.Get("/healthz", h.Health)

	r.Route("/v1", func(r chi.Router) {
		// Public, secret-free: meant to be polled directly by a
		// customer-facing frontend, possibly cross-origin.
		r.With(corsPublicGET, rateLimitMiddleware(publicLimiter, func(r *http.Request) string { return r.RemoteAddr })).
			Get("/orders/{orderID}", h.GetOrder)
		r.With(corsPublicGET, rateLimitMiddleware(publicLimiter, func(r *http.Request) string { return r.RemoteAddr })).
			Get("/chains", h.ListChains)

		// Order creation is server-to-server only: guarded by the shared
		// API key, never called from a browser.
		r.With(
			apiKeyMiddleware(apiKey),
			rateLimitMiddleware(ordersLimiter, func(r *http.Request) string { return r.RemoteAddr }),
		).Post("/orders", h.CreateOrder)
	})

	return r
}

func slogRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
