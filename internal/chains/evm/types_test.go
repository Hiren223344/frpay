package evm

import (
	"math/big"
	"testing"
)

func TestAddressToTopic(t *testing.T) {
	got := addressToTopic("0xA742cBEaaC8A9a58433154438E98F330447A4603")
	want := "0x000000000000000000000000a742cbeaac8a9a58433154438e98f330447a4603"
	if got != want {
		t.Fatalf("addressToTopic = %q, want %q", got, want)
	}
	if len(got) != 66 { // "0x" + 64 hex chars = 32 bytes
		t.Fatalf("topic length = %d, want 66 (32-byte value)", len(got))
	}
}

func TestParseHexBigInt(t *testing.T) {
	// 1,000,000 in 6-decimal USDT smallest units = 1.000000 USDT.
	got, err := parseHexBigInt("0x00000000000000000000000000000000000000000000000000000000000f4240")
	if err != nil {
		t.Fatalf("parseHexBigInt failed: %v", err)
	}
	if got.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("parseHexBigInt = %s, want 1000000", got.String())
	}
}

func TestParseHexQuantityAndToHexQuantity_RoundTrip(t *testing.T) {
	for _, n := range []int64{0, 1, 255, 4096, 123456789} {
		hexStr := toHexQuantity(n)
		got, err := parseHexQuantity(hexStr)
		if err != nil {
			t.Fatalf("parseHexQuantity(%q) failed: %v", hexStr, err)
		}
		if got != n {
			t.Fatalf("round-trip %d -> %q -> %d", n, hexStr, got)
		}
	}
}
