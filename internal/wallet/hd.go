// Package wallet derives and manages per-user USDC deposit addresses on
// Base via a BIP-32/BIP-44 HD wallet (ADR-027).
//
// SECURITY: the master mnemonic is the most sensitive secret in the
// system. It controls every deposit address. It lives only in the
// environment, never in Postgres, never in logs.
package wallet

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
)

// HDWallet derives deposit addresses from a master mnemonic using the
// standard Ethereum path m/44'/60'/0'/0/{index}.
type HDWallet struct {
	masterKey *bip32.Key
}

// NewFromMnemonic validates the mnemonic and builds the master key.
func NewFromMnemonic(mnemonic string) (*HDWallet, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, fmt.Errorf("invalid mnemonic")
	}

	seed := bip39.NewSeed(mnemonic, "")
	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		return nil, fmt.Errorf("derive master key: %w", err)
	}

	return &HDWallet{masterKey: masterKey}, nil
}

// DeriveAddress generates the deposit address at the given BIP-44 index.
// The returned private key is only needed for future sweeping; deposit
// detection never touches it.
func (w *HDWallet) DeriveAddress(index uint32) (common.Address, *ecdsa.PrivateKey, error) {
	path := []uint32{
		bip32.FirstHardenedChild + 44, // purpose: BIP-44
		bip32.FirstHardenedChild + 60, // coin: Ethereum
		bip32.FirstHardenedChild + 0,  // account: 0
		0,                             // external chain
		index,                         // address index
	}

	key := w.masterKey
	for _, p := range path {
		var err error
		key, err = key.NewChildKey(p)
		if err != nil {
			return common.Address{}, nil, fmt.Errorf("derive index %d: %w", index, err)
		}
	}

	privateKey, err := crypto.ToECDSA(key.Key)
	if err != nil {
		return common.Address{}, nil, fmt.Errorf("convert to ECDSA: %w", err)
	}

	publicKey, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		return common.Address{}, nil, fmt.Errorf("invalid public key type")
	}

	return crypto.PubkeyToAddress(*publicKey), privateKey, nil
}
