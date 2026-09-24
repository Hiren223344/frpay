package evm

import (
	"fmt"
	"math/big"
	"strings"
)

type rpcLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	TransactionHash string   `json:"transactionHash"`
}

func parseHexQuantity(hexStr string) (int64, error) {
	n := new(big.Int)
	if _, ok := n.SetString(strings.TrimPrefix(hexStr, "0x"), 16); !ok {
		return 0, fmt.Errorf("invalid hex quantity %q", hexStr)
	}
	return n.Int64(), nil
}

func parseHexBigInt(hexStr string) (*big.Int, error) {
	n := new(big.Int)
	if _, ok := n.SetString(strings.TrimPrefix(hexStr, "0x"), 16); !ok {
		return nil, fmt.Errorf("invalid hex big int %q", hexStr)
	}
	return n, nil
}

func toHexQuantity(n int64) string {
	return fmt.Sprintf("0x%x", n)
}

// addressToTopic pads a 20-byte address (with or without 0x prefix) out
// to a 32-byte, 0x-prefixed topic value as required by eth_getLogs
// indexed-parameter filters.
func addressToTopic(address string) string {
	addr := strings.ToLower(strings.TrimPrefix(address, "0x"))
	return "0x" + strings.Repeat("0", 24) + addr
}

// topicToAddress extracts the lowercase 0x-address from a 32-byte topic
// value (the last 20 bytes / 40 hex chars).
func topicToAddress(topic string) string {
	t := strings.TrimPrefix(topic, "0x")
	if len(t) < 40 {
		return "0x" + t
	}
	return "0x" + strings.ToLower(t[len(t)-40:])
}
