// Package sweep consolidates USDC from per-user deposit addresses into a
// treasury address (ADR-027 §9). Manual, operator-run tool: `sweep`
// reviews every deposit address balance on-chain and transfers anything
// above the reserve threshold to the treasury, paying gas from the
// address itself (each address must hold a little ETH).
//
// The ledger is never touched: sweeping moves on-chain funds only.
// User balances live in Postgres and are unaffected — same as a bank
// moving cash between its own vaults.
package sweep

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/wallet"
)

// Res sweep result for one address.
type Res struct {
	Address    string
	UserID     string
	SweptUSDC  float64
	TxHash     string
	SkippedMsg string
}

// Runner performs a sweep round.
type Runner struct {
	client   *ethclient.Client
	pg       *store.PostgresStore
	hd       *wallet.HDWallet
	usdc     common.Address
	treasury common.Address
	minUSD   float64
	chainID  int64
}

// New builds a sweep runner.
func New(rpcURL, usdcContract, treasuryAddr string, minUSD float64, chainID int64, pg *store.PostgresStore, hd *wallet.HDWallet) (*Runner, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("connect RPC: %w", err)
	}
	if !common.IsHexAddress(treasuryAddr) {
		return nil, fmt.Errorf("invalid treasury address: %s", treasuryAddr)
	}
	if usdcContract == "" {
		usdcContract = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" // USDC Base mainnet
	}
	return &Runner{
		client:   client,
		pg:       pg,
		hd:       hd,
		usdc:     common.HexToAddress(usdcContract),
		treasury: common.HexToAddress(treasuryAddr),
		minUSD:   minUSD,
		chainID:  chainID,
	}, nil
}

// Run sweeps all deposit addresses with a balance above the minimum.
// Dry-run mode reports what would happen without sending transactions.
func (r *Runner) Run(ctx context.Context, dryRun bool) ([]Res, error) {
	rows, err := r.pg.Pool.Query(ctx,
		`SELECT user_id, address, derivation_index FROM deposit_addresses
		 WHERE address IS NOT NULL ORDER BY derivation_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type entry struct {
		userID string
		addr   string
		idx    uint32
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.userID, &e.addr, &e.idx); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	var results []Res
	for _, e := range entries {
		res := Res{Address: e.addr, UserID: e.userID}

		addr := common.HexToAddress(e.addr)
		balMicro, err := r.usdcBalance(ctx, addr)
		if err != nil {
			res.SkippedMsg = fmt.Sprintf("balance check failed: %v", err)
			results = append(results, res)
			continue
		}
		balUSD := float64(balMicro) / 1e6

		if balUSD < r.minUSD {
			res.SkippedMsg = fmt.Sprintf("below minimum ($%.2f < $%.2f)", balUSD, r.minUSD)
			results = append(results, res)
			continue
		}

		res.SweptUSDC = balUSD

		if dryRun {
			res.TxHash = "(dry-run)"
			results = append(results, res)
			continue
		}

		// Derive the private key for this deposit address.
		_, priv, err := r.hd.DeriveAddress(e.idx)
		if err != nil {
			res.SkippedMsg = fmt.Sprintf("derive key failed: %v", err)
			results = append(results, res)
			continue
		}

		// The address needs ETH for gas.
		hasGas, err := r.hasGas(ctx, addr)
		if err != nil || !hasGas {
			res.SkippedMsg = "no ETH for gas — fund the address first (see ops runbook)"
			results = append(results, res)
			continue
		}

		txHash, err := r.transferUSDC(ctx, priv, balMicro)
		if err != nil {
			res.SkippedMsg = fmt.Sprintf("transfer failed: %v", err)
			results = append(results, res)
			continue
		}
		res.TxHash = txHash

		results = append(results, res)
	}

	return results, nil
}

// usdcBalance reads the ERC-20 balanceOf.
func (r *Runner) usdcBalance(ctx context.Context, addr common.Address) (int64, error) {
	data := calldataBalanceOf(addr)
	out, err := r.client.CallContract(ctx, ethereum.CallMsg{To: &r.usdc, Data: data}, nil)
	if err != nil {
		return 0, err
	}
	if len(out) != 32 {
		return 0, fmt.Errorf("unexpected balanceOf length %d", len(out))
	}
	bal := new(big.Int).SetBytes(out)
	if !bal.IsInt64() {
		return 0, fmt.Errorf("balance overflows int64")
	}
	return bal.Int64(), nil
}

// hasGas checks for enough ETH to pay a transfer (~60k gas).
func (r *Runner) hasGas(ctx context.Context, addr common.Address) (bool, error) {
	bal, err := r.client.BalanceAt(ctx, addr, nil)
	if err != nil {
		return false, err
	}
	// ~60000 gas * ~0.05 gwei tip + 0.001 base on Base ≈ tiny; require
	// 0.0005 ETH to be safe across gas spikes.
	min := new(big.Int).Mul(big.NewInt(5), big.NewInt(1e11)) // 0.0005 ETH
	return bal.Cmp(min) >= 0, nil
}

// transferUSDC signs and sends the sweep transfer to the treasury.
func (r *Runner) transferUSDC(ctx context.Context, key *ecdsa.PrivateKey, amountMicro int64) (string, error) {
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := r.client.PendingNonceAt(ctx, from)
	if err != nil {
		return "", err
	}
	gasPrice, err := r.client.SuggestGasPrice(ctx)
	if err != nil {
		return "", err
	}

	data := calldataTransfer(r.treasury, amountMicro)
	tx := types.NewTx(&types.LegacyTx{
		To:       &r.usdc,
		Value:    big.NewInt(0),
		Gas:      80_000,
		GasPrice: gasPrice,
		Data:     data,
		Nonce:    nonce,
	})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(r.chainID)), key)
	if err != nil {
		return "", err
	}
	if err := r.client.SendTransaction(ctx, signed); err != nil {
		return "", err
	}
	return signed.Hash().Hex(), nil
}

// calldataTransfer builds transfer(address,uint256) calldata.
func calldataTransfer(to common.Address, amountMicro int64) []byte {
	data := make([]byte, 0, 68)
	data = append(data, 0xa9, 0x05, 0x9c, 0xbb)
	var addrPad [32]byte
	copy(addrPad[12:], to.Bytes())
	data = append(data, addrPad[:]...)
	var amtPad [32]byte
	new(big.Int).SetInt64(amountMicro).FillBytes(amtPad[:])
	data = append(data, amtPad[:]...)
	return data
}

// calldataBalanceOf builds balanceOf(address) calldata.
func calldataBalanceOf(addr common.Address) []byte {
	data := make([]byte, 0, 36)
	data = append(data, 0x70, 0xa0, 0x82, 0x31)
	var pad [32]byte
	copy(pad[12:], addr.Bytes())
	data = append(data, pad[:]...)
	return data
}
