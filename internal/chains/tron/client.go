package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// client wraps TronGrid's REST API with automatic fallback across a
// configured list of endpoints and exponential backoff, so a single
// rate-limited or unreachable provider doesn't stall detection.
type client struct {
	baseURLs   []string // primary first, then fallbacks
	apiKey     string
	httpClient *http.Client
}

func newClient(primary string, fallbacks []string, apiKey string) *client {
	urls := append([]string{primary}, fallbacks...)
	return &client{
		baseURLs: urls,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

const maxAttemptsPerURL = 2

// get issues a GET against path+query, trying each configured base URL in
// order with exponential backoff, until one succeeds or all are
// exhausted.
func (c *client) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	var lastErr error
	for _, base := range c.baseURLs {
		backoff := 500 * time.Millisecond
		for attempt := 0; attempt < maxAttemptsPerURL; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff *= 2
			}

			reqURL := base + path
			if len(query) > 0 {
				reqURL += "?" + query.Encode()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
			if err != nil {
				lastErr = err
				continue
			}
			if c.apiKey != "" {
				req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
			}

			resp, err := c.httpClient.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("request %s: %w", reqURL, err)
				continue
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("read response from %s: %w", reqURL, readErr)
				continue
			}

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("%s returned %d: %s", reqURL, resp.StatusCode, truncate(body, 200))
				continue
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s returned %d: %s", reqURL, resp.StatusCode, truncate(body, 200))
			}

			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				lastErr = fmt.Errorf("decode response from %s: %w", reqURL, err)
				continue
			}
			return nil
		}
	}
	return fmt.Errorf("all TronGrid endpoints failed: %w", lastErr)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
