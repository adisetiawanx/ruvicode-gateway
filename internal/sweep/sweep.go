package sweep

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/wallet"
)

type Res struct {
	Address    string
	UserID     string
	SweptUSDC  float64
	TxHash     string
	SkippedMsg string
	GasFunded  float64 // ETH sent from treasury to this address for gas
}

type Runner struct {
	client      *ethclient.Client
	pg          *store.PostgresStore
	hd          *wallet.HDWallet
	usdc        common.Address
	treasury    common.Address
	treasuryPriv *ecdsa.PrivateKey
	minUSD      float64
	chainID     int64
}

func New(rpcURL, usdcContract, treasuryAddr string, minUSD float64, chainID int64, pg *store.PostgresStore, hd *wallet.HDWallet) (*Runner, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("connect RPC: %w", err)
	}
	if !common.IsHexAddress(treasuryAddr) {
		return nil, fmt.Errorf("invalid treasury address: %s", treasuryAddr)
	}
	if usdcContract == "" {
		usdcContract = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
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

// SetTreasuryKey sets the treasury private key for gas funding.
// Without this, execute=true will skip addresses that lack ETH with
// a clear error instead of funding them.
func (r *Runner) SetTreasuryKey(key *ecdsa.PrivateKey) {
	r.treasuryPriv = key
}

// HasTreasuryKey reports whether gas funding is available.
func (r *Runner) HasTreasuryKey() bool { return r.treasuryPriv != nil }

func (r *Runner) Run(ctx context.Context, dryRun bool) ([]Res, error) {
	rows, err := r.pg.Pool.Query(ctx, `SELECT user_id, address, derivation_index FROM deposit_addresses WHERE address IS NOT NULL ORDER BY derivation_index`)
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
		if err := rows.Scan(&e.userID, &e.addr, &e.idx); err == nil {
			entries = append(entries, e)
		}
	}
	results := make([]Res, 0, len(entries))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // lower concurrency: gas funding sends real txs
	for _, e := range entries {
		e := e
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := r.processEntry(ctx, e, dryRun)
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results, nil
}

func (r *Runner) processEntry(ctx context.Context, e struct {
	userID string
	addr   string
	idx    uint32
}, dryRun bool) Res {
	res := Res{Address: e.addr, UserID: e.userID}
	addr := common.HexToAddress(e.addr)
	balMicro, err := r.usdcBalance(ctx, addr)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("balance check failed: %v", err)
		return res
	}
	balUSD := float64(balMicro) / 1e6
	if balUSD < r.minUSD {
		res.SkippedMsg = fmt.Sprintf("below minimum ($%.2f < $%.2f)", balUSD, r.minUSD)
		return res
	}
	res.SweptUSDC = balUSD
	if dryRun {
		res.TxHash = "(dry-run)"
		return res
	}
	_, priv, err := r.hd.DeriveAddress(e.idx)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("derive key failed: %v", err)
		return res
	}

	// Check if deposit address has enough ETH for gas.
	gasPerTx, err := r.estimatedGasEth(ctx)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("gas estimate failed: %v", err)
		return res
	}
	depositEth, err := r.EthBalance(ctx, e.addr)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("eth balance check failed: %v", err)
		return res
	}

	// Gas funding: if deposit address lacks ETH, treasury sends ETH first.
	if depositEth < gasPerTx {
		if r.treasuryPriv == nil {
			res.SkippedMsg = "no ETH for gas and treasury key not configured"
			return res
		}
		// Send the full gas amount (not just the deficit) to keep it simple
		// and avoid rounding issues. ~0.0001 ETH is negligible.
		fundAmount := gasPerTx
		treasuryEth, err := r.EthBalance(ctx, r.treasury.Hex())
		if err != nil {
			res.SkippedMsg = fmt.Sprintf("treasury eth check failed: %v", err)
			return res
		}
		if treasuryEth < fundAmount {
			res.SkippedMsg = fmt.Sprintf("treasury lacks ETH for gas funding (need %.6f, have %.6f)", fundAmount, treasuryEth)
			return res
		}
		fundTxHash, err := r.fundGas(ctx, addr, fundAmount)
		if err != nil {
			res.SkippedMsg = fmt.Sprintf("gas funding failed: %v", err)
			return res
		}
		res.GasFunded = fundAmount
		// Wait for the gas funding tx to be mined before attempting the USDC transfer.
		if err := r.waitForReceipt(ctx, fundTxHash, 30); err != nil {
			res.SkippedMsg = fmt.Sprintf("gas funding receipt failed: %v", err)
			return res
		}
	}

	txHash, err := r.transferUSDC(ctx, priv, balMicro)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("transfer failed: %v", err)
		return res
	}
	res.TxHash = txHash
	return res
}

func (r *Runner) ChainID() int64       { return r.chainID }
func (r *Runner) UsdcContract() string { return r.usdc.Hex() }
func (r *Runner) TreasuryAddr() string { return r.treasury.Hex() }

// EthBalance returns the ETH balance of an address as a float in whole ETH.
func (r *Runner) EthBalance(ctx context.Context, addrHex string) (float64, error) {
	bal, err := r.client.BalanceAt(ctx, common.HexToAddress(addrHex), nil)
	if err != nil {
		return 0, err
	}
	return weiToEth(bal), nil
}

// UsdcBalanceOf returns the USDC balance of an address in whole dollars.
func (r *Runner) UsdcBalanceOf(ctx context.Context, addrHex string) (float64, error) {
	micro, err := r.usdcBalance(ctx, common.HexToAddress(addrHex))
	if err != nil {
		return 0, err
	}
	return float64(micro) / 1e6, nil
}

// EstimatedGasPerTxEth estimates the gas cost of one USDC transfer in ETH.
func (r *Runner) EstimatedGasPerTxEth(ctx context.Context) (float64, error) {
	return r.estimatedGasEth(ctx)
}

func (r *Runner) estimatedGasEth(ctx context.Context) (float64, error) {
	price, err := r.client.SuggestGasPrice(ctx)
	if err != nil {
		return 0, err
	}
	return weiToEth(new(big.Int).Mul(big.NewInt(80_000), price)), nil
}

func weiToEth(wei *big.Int) float64 {
	f := new(big.Float).SetInt(wei)
	out, _ := new(big.Float).Quo(f, big.NewFloat(1e18)).Float64()
	return out
}

func (r *Runner) usdcBalance(ctx context.Context, addr common.Address) (int64, error) {
	out, err := r.client.CallContract(ctx, ethereum.CallMsg{To: &r.usdc, Data: calldataBalanceOf(addr)}, nil)
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

// fundGas sends ETH from the treasury to a deposit address for gas.
func (r *Runner) fundGas(ctx context.Context, to common.Address, amountEth float64) (string, error) {
	if r.treasuryPriv == nil {
		return "", fmt.Errorf("treasury private key not set")
	}
	from := crypto.PubkeyToAddress(r.treasuryPriv.PublicKey)
	nonce, err := r.client.PendingNonceAt(ctx, from)
	if err != nil {
		return "", err
	}
	gasPrice, err := r.client.SuggestGasPrice(ctx)
	if err != nil {
		return "", err
	}
	// Convert ETH float to wei as a big.Int.
	// Multiply by 1e18 using big.Float for precision, then convert.
	ethFloat := new(big.Float).SetFloat64(amountEth)
	weiFloat := new(big.Float).Mul(ethFloat, big.NewFloat(1e18))
	amountWei, _ := weiFloat.Int(nil)
	// Use a fixed gas limit for simple ETH transfer (21000).
	tx := types.NewTx(&types.LegacyTx{
		To:       &to,
		Value:    amountWei,
		Gas:      21_000,
		GasPrice: gasPrice,
		Nonce:    nonce,
	})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(r.chainID)), r.treasuryPriv)
	if err != nil {
		return "", err
	}
	if err := r.client.SendTransaction(ctx, signed); err != nil {
		return "", err
	}
	return signed.Hash().Hex(), nil
}

// waitForReceipt polls for a transaction receipt up to maxWaitSeconds.
func (r *Runner) waitForReceipt(ctx context.Context, txHash string, maxWaitSeconds int) error {
	hash := common.HexToHash(txHash)
	deadline := time.Now().Add(time.Duration(maxWaitSeconds) * time.Second)
	for time.Now().Before(deadline) {
		receipt, err := r.client.TransactionReceipt(ctx, hash)
		if err == nil {
			if receipt.Status == types.ReceiptStatusSuccessful {
				return nil
			}
			return fmt.Errorf("tx %s failed (status %d)", txHash, receipt.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for receipt %s", txHash)
}

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
	tx := types.NewTx(&types.LegacyTx{To: &r.usdc, Value: big.NewInt(0), Gas: 80_000, GasPrice: gasPrice, Nonce: nonce, Data: calldataTransfer(r.treasury, amountMicro)})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(r.chainID)), key)
	if err != nil {
		return "", err
	}
	if err := r.client.SendTransaction(ctx, signed); err != nil {
		return "", err
	}
	return signed.Hash().Hex(), nil
}

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

func calldataBalanceOf(addr common.Address) []byte {
	data := make([]byte, 0, 36)
	data = append(data, 0x70, 0xa0, 0x82, 0x31)
	var pad [32]byte
	copy(pad[12:], addr.Bytes())
	data = append(data, pad[:]...)
	return data
}
