package main

// Dispersal test: sends real testnet USDC from deposit address index 0 to
// five other derived deposit addresses, simulating five independent user
// deposits on Base Sepolia. Run against the TEST mnemonic only.

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ruvicode/gateway/internal/wallet"
)

const (
	rpcURL       = "https://base-sepolia.g.alchemy.com/v2/alch_6gI71G6fQOoBP_pKY38CT"
	usdcContract = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	chainID      = 84532
)

// Minimal ERC-20 transfer calldata: transfer(address,uint256)
func transferCalldata(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 0, 68)
	data = append(data, 0xa9, 0x05, 0x9c, 0xbb) // transfer(address,uint256)
	addrBytes := make([]byte, 32)
	copy(addrBytes[12:], to.Bytes())
	data = append(data, addrBytes...)
	amtBytes := make([]byte, 32)
	amount.FillBytes(amtBytes)
	data = append(data, amtBytes...)
	return data
}

func main() {
	mnemonic := os.Args[1]
	if mnemonic == "" {
		fmt.Println("usage: disperse <mnemonic>")
		os.Exit(1)
	}

	hd, err := wallet.NewFromMnemonic(mnemonic)
	if err != nil {
		panic(err)
	}

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		panic(err)
	}

	// Funder = index 0.
	funderAddr, funderKey, err := hd.DeriveAddress(0)
	if err != nil {
		panic(err)
	}
	fmt.Printf("funder: %s\n", funderAddr.Hex())

	// Recipients: the five deposit addresses registered in the test DB
	// (users e2e-live-0 .. e2e-live-4, derived at indices 200..204).
	type target struct {
		userID string
		idx    uint32
		amount float64
	}
	targets := []target{
		{"e2e-live-0", 200, 1.23},
		{"e2e-live-1", 201, 2.00},
		{"e2e-live-2", 202, 0.75},
		{"e2e-live-3", 203, 3.50},
		{"e2e-live-4", 204, 4.20},
	}

	usdc := common.HexToAddress(usdcContract)
	nonce, err := client.PendingNonceAt(context.Background(), funderAddr)
	if err != nil {
		panic(err)
	}

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		panic(err)
	}

	chainIDBig := big.NewInt(chainID)
	_ = chainIDBig

	for _, tg := range targets {
		addr, _, err := hd.DeriveAddress(tg.idx)
		if err != nil {
			panic(err)
		}

		amountMicro := new(big.Int).Mul(big.NewInt(int64(tg.amount*100)), big.NewInt(10_000)) // dollars -> micro (6dp)
		data := transferCalldata(addr, amountMicro)

		tx := types.NewTx(&types.LegacyTx{
			To:       &usdc,
			Value:    big.NewInt(0),
			Gas:      200_000,
			GasPrice: gasPrice,
			Data:     data,
			Nonce:    nonce,
		})
		signedTx, err := signTx(funderKey, tx)
		if err != nil {
			panic(err)
		}

		if err := client.SendTransaction(context.Background(), signedTx); err != nil {
			fmt.Printf("FAIL %s (%s): %v\n", tg.userID, addr.Hex(), err)
			continue
		}
		fmt.Printf("SENT %s -> %s amount=%.2f tx=%s\n", tg.userID, addr.Hex(), tg.amount, signedTx.Hash().Hex())
		nonce++
	}

	fmt.Println("done")
}

func signTx(key *ecdsa.PrivateKey, tx *types.Transaction) (*types.Transaction, error) {
	s := types.NewEIP155Signer(big.NewInt(chainID))
	return types.SignTx(tx, s, key)
}
