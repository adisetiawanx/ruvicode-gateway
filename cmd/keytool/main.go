// keytool derives addresses and private keys from the HD wallet mnemonic.
//
// Usage:
//   keytool <mnemonic>              show deposit address #0 + treasury
//   keytool <mnemonic> <index>      show address + private key at index
//   keytool <mnemonic> treasury     show treasury address + private key
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"

	"github.com/ruvicode/gateway/internal/wallet"
)

const treasuryIndex = 0x7FFFFFFF

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: keytool <mnemonic> [index|treasury]")
		os.Exit(1)
	}

	hd, err := wallet.NewFromMnemonic(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid mnemonic: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 3 {
		// default: show index 0 and treasury
		addr0, _, _ := hd.DeriveAddress(0)
		addrT, _, _ := hd.DeriveAddress(treasuryIndex)
		fmt.Println("Deposit address #0:")
		fmt.Println("  index   0")
		fmt.Println("  address", addr0.Hex())
		fmt.Println()
		fmt.Println("Treasury (reserved):")
		fmt.Println("  index   2147483647 (0x7FFFFFFF)")
		fmt.Println("  address", addrT.Hex())
		return
	}

	var idx uint32
	if os.Args[2] == "treasury" {
		idx = treasuryIndex
	} else {
		n, err := strconv.ParseUint(os.Args[2], 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid index: %v\n", err)
			os.Exit(1)
		}
		idx = uint32(n)
	}

	addr, priv, err := hd.DeriveAddress(idx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "derive failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Address:    ", addr.Hex())
	fmt.Println("Private key:", "0x"+hex.EncodeToString(priv.D.Bytes()))
}
