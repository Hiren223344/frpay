package tron

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// TestAddressToHex_RoundTrip decodes real, well-known TRON addresses to
// hex and re-encodes the same bytes independently (via base58 directly,
// not through addressToHex) to confirm the decode is byte-correct — not
// just "didn't error". A decode bug that silently produced the wrong
// bytes would still round-trip incorrectly and get caught here.
func TestAddressToHex_RoundTrip(t *testing.T) {
	addresses := []string{
		// The default TRON_USDT_CONTRACT in .env.example — as
		// independently verifiable a real TRON address as exists.
		"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
	}
	for _, addr := range addresses {
		t.Run(addr, func(t *testing.T) {
			gotHex, err := addressToHex(addr)
			if err != nil {
				t.Fatalf("addressToHex(%q) failed: %v", addr, err)
			}

			payload, err := hex.DecodeString(gotHex)
			if err != nil {
				t.Fatalf("addressToHex returned invalid hex %q: %v", gotHex, err)
			}
			if len(payload) != 21 {
				t.Fatalf("decoded payload is %d bytes, want 21", len(payload))
			}
			if payload[0] != 0x41 {
				t.Fatalf("decoded payload version byte = 0x%x, want 0x41", payload[0])
			}

			// Re-encode independently and confirm we get back the exact
			// original address.
			checksum := doubleSHA256(payload)[:4]
			reencoded := base58.Encode(append(append([]byte{}, payload...), checksum...))
			if reencoded != addr {
				t.Fatalf("round-trip mismatch: decoded %s then re-encoded to %s", addr, reencoded)
			}
		})
	}
}

func TestAddressToHex_RejectsBadChecksum(t *testing.T) {
	// Flip the last character of a valid address; base58check must
	// reject it.
	bad := "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6g"
	if _, err := addressToHex(bad); err == nil {
		t.Fatalf("addressToHex(%q) succeeded, want checksum error", bad)
	}
}

func TestAddressToHex_RejectsWrongLength(t *testing.T) {
	if _, err := addressToHex("not-a-real-address"); err == nil {
		t.Fatalf("addressToHex accepted garbage input")
	}
}
