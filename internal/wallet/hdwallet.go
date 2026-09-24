// Package wallet derives per-order deposit addresses from a watch-only
// BIP44 extended public key (xpub). This service is given the xpub for
// each chain family's *change-level* path — m/44'/195'/0'/0 for TRON,
// m/44'/60'/0'/0 for EVM chains — exported from an offline signer. Only
// non-hardened child derivation is possible from an xpub, which is
// exactly what's needed here: derivation_index becomes the child index
// directly, and no private key or hardened path segment is ever
// reconstructable from what this service holds. Frenix Pay can compute
// every deposit address that has ever been issued, and can never move
// a single unit of the funds sent to them.
package wallet

import (
	"fmt"

	"github.com/btcsuite/btcd/btcutil/hdkeychain"
)

// ChainFamily groups chains that share one address-derivation scheme.
// TRON has its own; every EVM chain (Ethereum, BSC, Polygon, ...) shares
// one because the secp256k1 pubkey -> address transform is identical
// across them, so one xpub covers all EVM chains at once.
type ChainFamily string

const (
	FamilyTron ChainFamily = "tron"
	FamilyEVM  ChainFamily = "evm"
)

// HDWallet holds only extended *public* keys, one per chain family.
type HDWallet struct {
	keys map[ChainFamily]*hdkeychain.ExtendedKey
}

// New parses the configured xpubs. Any xpub containing a private key
// extension (an xprv) is rejected — this service must never be handed
// spend authority.
func New(xpubs map[ChainFamily]string) (*HDWallet, error) {
	w := &HDWallet{keys: make(map[ChainFamily]*hdkeychain.ExtendedKey)}
	for family, xpub := range xpubs {
		if xpub == "" {
			continue
		}
		key, err := hdkeychain.NewKeyFromString(xpub)
		if err != nil {
			return nil, fmt.Errorf("parse xpub for %s: %w", family, err)
		}
		if key.IsPrivate() {
			return nil, fmt.Errorf("refusing to load a private extended key for %s: this service must only ever hold a watch-only xpub", family)
		}
		w.keys[family] = key
	}
	return w, nil
}

// DerivePublicKey returns the uncompressed secp256k1 public key bytes
// (0x04 || X || Y) for the given chain family and non-hardened child
// index.
func (w *HDWallet) DerivePublicKey(family ChainFamily, index uint32) ([]byte, error) {
	parent, ok := w.keys[family]
	if !ok {
		return nil, fmt.Errorf("no xpub configured for chain family %q", family)
	}
	if index >= hdkeychain.HardenedKeyStart {
		return nil, fmt.Errorf("derivation index %d is in the hardened range; only non-hardened derivation is possible from an xpub", index)
	}
	child, err := parent.Derive(index)
	if err != nil {
		return nil, fmt.Errorf("derive child %d: %w", index, err)
	}
	pubKey, err := child.ECPubKey()
	if err != nil {
		return nil, fmt.Errorf("extract pubkey at index %d: %w", index, err)
	}
	return pubKey.SerializeUncompressed(), nil
}
