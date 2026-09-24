// Command democheckout is a minimal stand-in for a real merchant's own
// backend (e.g. frenix-back-v3). It holds the Frenix Pay API
// key/secret — which must never reach a browser — creates orders on the
// customer's behalf, and serves the static checkout page. This is the
// intended integration shape: the browser only ever talks to the
// merchant's own domain plus Frenix Pay's public GET /v1/orders/{id}
// and GET /v1/chains endpoints, never the merchant-authenticated ones
// directly.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hiren223344/frpay/internal/merchant"
)

type createOrderRequest struct {
	AmountUSD string `json:"amount_usd"`
	Chain     string `json:"chain,omitempty"`
}

type server struct {
	frenixPayBaseURL string
	apiKey           string
	apiSecret        string
	httpClient       *http.Client
	logger           *slog.Logger
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	listenAddr := getenvDefault("LISTEN_ADDR", ":8090")
	staticDir := getenvDefault("STATIC_DIR", "web/checkout")
	baseURL := getenvDefault("FRENIXPAY_BASE_URL", "http://localhost:8080")
	apiKey := os.Getenv("FRENIXPAY_API_KEY")
	apiSecret := os.Getenv("FRENIXPAY_API_SECRET")

	if apiKey == "" || apiSecret == "" {
		logger.Error("FRENIXPAY_API_KEY and FRENIXPAY_API_SECRET are required (create one with: frenixpay -create-merchant=\"Acme Studio\")")
		os.Exit(1)
	}

	srv := &server{
		frenixPayBaseURL: baseURL,
		apiKey:           apiKey,
		apiSecret:        apiSecret,
		httpClient:       &http.Client{Timeout: 15 * time.Second},
		logger:           logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkout/create-order", srv.createOrder)
	mux.Handle("/", http.FileServer(http.Dir(staticDir)))

	logger.Info("demo merchant server starting", "addr", listenAddr, "static_dir", staticDir, "frenixpay_base_url", baseURL)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func (s *server) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.AmountUSD == "" {
		http.Error(w, `{"error":"amount_usd is required"}`, http.StatusBadRequest)
		return
	}

	body, err := json.Marshal(req)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	const path = "/v1/orders"
	timestamp := time.Now().Unix()
	signature := merchant.Sign(s.apiSecret, http.MethodPost, path, timestamp, body)

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.frenixPayBaseURL+path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("X-Frenix-Key", s.apiKey)
	upstreamReq.Header.Set("X-Frenix-Timestamp", fmt.Sprintf("%d", timestamp))
	upstreamReq.Header.Set("X-Frenix-Signature", signature)

	resp, err := s.httpClient.Do(upstreamReq)
	if err != nil {
		s.logger.Error("call to frenixpay failed", "error", err)
		http.Error(w, `{"error":"payment service unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// The order-creation response carries no merchant secret, so it's
	// safe to pass straight through to the browser as-is.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
