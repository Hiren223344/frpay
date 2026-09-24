package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/merchant"
	"github.com/hiren223344/frpay/internal/orders"
	"github.com/hiren223344/frpay/internal/ratelimit"
)

func NewRouter(ordersSvc *orders.Service, merchantSvc *merchant.Service, chainMgr *chains.Manager, merchantLimiter, publicLimiter *ratelimit.Limiter, logger *slog.Logger) http.Handler {
	h := NewHandlers(ordersSvc, merchantSvc, chainMgr, logger)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(slogRequestLogger(logger))

	r.Get("/healthz", h.Health)

	r.Route("/v1", func(r chi.Router) {
		// These two endpoints carry no secret and are meant to be polled
		// directly by a checkout page's browser JS, which is typically
		// served from the merchant's own origin (not this service's) —
		// so they're open to any origin. Every other route stays
		// same-origin/server-to-server only; CORS is deliberately not
		// enabled globally.
		r.With(
			corsPublicGET,
			rateLimitMiddleware(publicLimiter, func(r *http.Request) string { return r.RemoteAddr }),
		).Get("/orders/{orderID}", h.GetOrder)

		r.With(
			corsPublicGET,
			rateLimitMiddleware(publicLimiter, func(r *http.Request) string { return r.RemoteAddr }),
		).Get("/chains", h.ListChains)

		r.Group(func(r chi.Router) {
			r.Use(authMiddleware(merchantSvc, logger))
			r.Use(rateLimitMiddleware(merchantLimiter, func(r *http.Request) string {
				m := merchantFromContext(r.Context())
				if m == nil {
					return r.RemoteAddr
				}
				return m.ID.String()
			}))
			r.Post("/orders", h.CreateOrder)
			r.Post("/webhooks/register", h.RegisterWebhook)
		})
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
