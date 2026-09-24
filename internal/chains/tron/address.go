package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// addressToHex decodes a base58check TRON address (e.g.
// "TRp5otn82XoT37csQQTQjGyMB7o8646u6f") into the lowercase hex form
// TronGrid uses in its event `result.to` field (the 0x41-prefixed
// 21-byte payload, no "0x" prefix) — done once at startup so every
// incoming event can be matched with a plain string comparison instead
// of re-decoding on every poll.
func addressToHex(base58Addr string) (string, error) {
	full := base58.Decode(base58Addr)
	if len(full) != 25 {
		return "", fmt.Errorf("invalid TRON address %q: decoded to %d bytes, want 25", base58Addr, len(full))
	}
	payload, checksum := full[:21], full[21:]
	if got := doubleSHA256(payload)[:4]; string(got) != string(checksum) {
		return "", fmt.Errorf("invalid TRON address %q: checksum mismatch", base58Addr)
	}
	if payload[0] != 0x41 {
		return "", fmt.Errorf("invalid TRON address %q: unexpected version byte 0x%x", base58Addr, payload[0])
	}
	return hex.EncodeToString(payload), nil
}

func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}
