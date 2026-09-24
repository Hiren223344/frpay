package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Sign computes the signature Frenix Pay sends with every outbound
// webhook: hex(HMAC-SHA256(secret, "{timestamp}.{body}")). Merchants
// verify it the same way to authenticate that a callback genuinely came
// from Frenix Pay.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
