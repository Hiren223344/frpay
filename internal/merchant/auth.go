package merchant

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Sign computes the HMAC-SHA256 signature a merchant must send with every
// request: hex(HMAC(secret, "{timestamp}.{method}.{path}.{body}")).
// Binding the method, path and timestamp (not just the body) stops a
// captured signature from being replayed against a different endpoint.
func Sign(secret, method, path string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s.%s.", timestamp, method, path)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify does a constant-time comparison of the provided signature
// against the one this service computes from the same inputs.
func Verify(secret, method, path string, timestamp int64, body []byte, providedSignature string) bool {
	expected := Sign(secret, method, path, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(providedSignature))
}
