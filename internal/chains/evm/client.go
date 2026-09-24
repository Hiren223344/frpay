package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// client is a minimal JSON-RPC client with fallback across a configured
// list of endpoints and exponential backoff, mirroring the TRON client's
// resilience shape so both reference implementations behave the same way
// under RPC rate limits or outages.
type client struct {
	urls       []string // primary first, then fallbacks
	httpClient *http.Client
}

func newClient(primary string, fallbacks []string) *client {
	return &client{
		urls:       append([]string{primary}, fallbacks...),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const maxAttemptsPerURL = 2

func (c *client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal rpc request: %w", err)
	}

	var lastErr error
	for _, url := range c.urls {
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

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				lastErr = err
				continue
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				lastErr = fmt.Errorf("request %s: %w", url, err)
				continue
			}

			var rpcResp rpcResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&rpcResp)
			resp.Body.Close()

			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("%s returned status %d", url, resp.StatusCode)
				continue
			}
			if decodeErr != nil {
				lastErr = fmt.Errorf("decode response from %s: %w", url, decodeErr)
				continue
			}
			if rpcResp.Error != nil {
				lastErr = fmt.Errorf("%s rpc error %d: %s", url, rpcResp.Error.Code, rpcResp.Error.Message)
				continue
			}
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(rpcResp.Result, out); err != nil {
				lastErr = fmt.Errorf("unmarshal result from %s: %w", url, err)
				continue
			}
			return nil
		}
	}
	return fmt.Errorf("all RPC endpoints failed: %w", lastErr)
}
