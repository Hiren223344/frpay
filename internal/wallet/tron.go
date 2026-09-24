package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/base58"
)

const tronAddressVersion = 0x41

// TronAddress computes the base58check TRON address for a given
// uncompressed pubkey. TRON reuses the Ethereum keccak256(pubkey)[-20:]
// scheme for the account bytes, then base58check-encodes them with the
// 0x41 version byte instead of rendering as 0x-hex.
func TronAddress(uncompressedPubKey []byte) (string, error) {
	if len(uncompressedPubKey) != 65 || uncompressedPubKey[0] != 0x04 {
		return "", fmt.Errorf("expected 65-byte uncompressed pubkey with 0x04 prefix, got %d bytes", len(uncompressedPubKey))
	}
	hash := keccak256(uncompressedPubKey[1:])
	addrBytes := append([]byte{tronAddressVersion}, hash[len(hash)-20:]...)
	return base58CheckEncode(addrBytes), nil
}

// TronAddressFromHex converts a TRON hex-form address (21 bytes: the
// 0x41 version byte followed by the 20-byte account hash, as returned
// by TronGrid's decoded event `result` fields) into base58check form.
func TronAddressFromHex(hexAddr string) (string, error) {
	raw, err := hex.DecodeString(hexAddr)
	if err != nil {
		return "", fmt.Errorf("decode hex address: %w", err)
	}
	if len(raw) != 21 || raw[0] != tronAddressVersion {
		return "", fmt.Errorf("unexpected TRON address bytes: len=%d version=%x", len(raw), raw[0])
	}
	return base58CheckEncode(raw), nil
}

func base58CheckEncode(payload []byte) string {
	checksum := doubleSHA256(payload)[:4]
	full := append(append([]byte{}, payload...), checksum...)
	return base58.Encode(full)
}

func doubleSHA256(data []byte) []byte {
	first := sha256.Sum256(data)
	second := sha256.Sum256(first[:])
	return second[:]
}
