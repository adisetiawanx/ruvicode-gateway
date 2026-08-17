package sweep

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"sync"

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
}

type Runner struct {
	client   *ethclient.Client
	pg       *store.PostgresStore
	hd       *wallet.HDWallet
	usdc     common.Address
	treasury common.Address
	minUSD   float64
	chainID  int64
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
	return &Runner{client: client, pg: pg, hd: hd, usdc: common.HexToAddress(usdcContract), treasury: common.HexToAddress(treasuryAddr), minUSD: minUSD, chainID: chainID}, nil
}

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
	sem := make(chan struct{}, 8)
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
	hasGas, err := r.hasGas(ctx, addr)
	if err != nil || !hasGas {
		res.SkippedMsg = "no ETH for gas — fund the address first"
		return res
	}
	txHash, err := r.transferUSDC(ctx, priv, balMicro)
	if err != nil {
		res.SkippedMsg = fmt.Sprintf("transfer failed: %v", err)
		return res
	}
	res.TxHash = txHash
	return res
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

func (r *Runner) hasGas(ctx context.Context, addr common.Address) (bool, error) {
	bal, err := r.client.BalanceAt(ctx, addr, nil)
	if err != nil {
		return false, err
	}
	min := new(big.Int).Mul(big.NewInt(5), big.NewInt(1e11))
	return bal.Cmp(min) >= 0, nil
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
