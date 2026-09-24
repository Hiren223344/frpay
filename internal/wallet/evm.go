package wallet

import (
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/sha3"
)

// EVMAddress computes the checksummed 0x address shared by every EVM
// chain (Ethereum, BSC, Polygon, ...) for a given uncompressed pubkey.
func EVMAddress(uncompressedPubKey []byte) (string, error) {
	if len(uncompressedPubKey) != 65 || uncompressedPubKey[0] != 0x04 {
		return "", fmt.Errorf("expected 65-byte uncompressed pubkey with 0x04 prefix, got %d bytes", len(uncompressedPubKey))
	}
	hash := keccak256(uncompressedPubKey[1:]) // drop the 0x04 prefix
	addrBytes := hash[len(hash)-20:]
	return toChecksumAddress(addrBytes), nil
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// toChecksumAddress implements EIP-55 mixed-case checksum encoding.
func toChecksumAddress(addr []byte) string {
	hexAddr := hex.EncodeToString(addr)
	hash := keccak256([]byte(hexAddr))
	hashHex := hex.EncodeToString(hash)

	out := make([]byte, len(hexAddr))
	for i, c := range []byte(hexAddr) {
		if c >= '0' && c <= '9' {
			out[i] = c
			continue
		}
		// c is a-f; uppercase it if the corresponding checksum hash nibble >= 8
		nibble := hashHex[i]
		var v int
		if nibble >= '0' && nibble <= '9' {
			v = int(nibble - '0')
		} else {
			v = int(nibble-'a') + 10
		}
		if v >= 8 {
			out[i] = c - 32 // to uppercase
		} else {
			out[i] = c
		}
	}
	return "0x" + string(out)
}
