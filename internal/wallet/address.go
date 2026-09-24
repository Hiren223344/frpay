package wallet

import "fmt"

// DeriveAddress derives the child pubkey at index for the given chain
// family and encodes it into that family's native address format.
func (w *HDWallet) DeriveAddress(family ChainFamily, index uint32) (string, error) {
	pubKey, err := w.DerivePublicKey(family, index)
	if err != nil {
		return "", err
	}
	switch family {
	case FamilyTron:
		return TronAddress(pubKey)
	case FamilyEVM:
		return EVMAddress(pubKey)
	default:
		return "", fmt.Errorf("unsupported chain family %q", family)
	}
}
