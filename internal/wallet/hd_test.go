package wallet

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// Deterministic test mnemonic — NEVER use for real funds.
const testMnemonic = "test test test test test test test test test test test junk"

// Known-good derivation vector: this mnemonic with an empty passphrase is
// the standard Hardhat/Foundry account #19 address at index 0.
const expectedIndex0 = "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"

func TestDeriveAddressDeterministic(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic)
	if err != nil {
		t.Fatalf("NewFromMnemonic: %v", err)
	}

	a1, _, err := w.DeriveAddress(0)
	if err != nil {
		t.Fatalf("derive 0: %v", err)
	}
	a2, _, err := w.DeriveAddress(0)
	if err != nil {
		t.Fatalf("derive 0 again: %v", err)
	}
	if a1 != a2 {
		t.Errorf("derivation not deterministic: %s != %s", a1.Hex(), a2.Hex())
	}
}

func TestDeriveAddressKnownVector(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic)
	if err != nil {
		t.Fatalf("NewFromMnemonic: %v", err)
	}

	addr, _, err := w.DeriveAddress(0)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if addr.Hex() != expectedIndex0 {
		t.Errorf("index 0 = %s, want %s (BIP-44 m/44'/60'/0'/0/0 vector)", addr.Hex(), expectedIndex0)
	}
}

func TestDeriveAddressUniquePerIndex(t *testing.T) {
	w, err := NewFromMnemonic(testMnemonic)
	if err != nil {
		t.Fatalf("NewFromMnemonic: %v", err)
	}

	seen := make(map[common.Address]bool)
	for i := uint32(0); i < 5; i++ {
		addr, _, err := w.DeriveAddress(i)
		if err != nil {
			t.Fatalf("derive %d: %v", i, err)
		}
		if seen[addr] {
			t.Errorf("index %d produced a duplicate address %s", i, addr.Hex())
		}
		seen[addr] = true
	}
}

func TestInvalidMnemonicRejected(t *testing.T) {
	if _, err := NewFromMnemonic("not a valid mnemonic at all"); err == nil {
		t.Error("invalid mnemonic accepted")
	}
}
