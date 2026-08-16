package monitor

import (
	"context"
	"math/big"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/wallet"
)

// ─── Test harness: real test DB (ruvicode_test) + deterministic log builder ───

const testMnemonic = "test test test test test test test test test test test junk"

var transferEventSig = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// buildTransferLog constructs a USDC Transfer event log like the chain would emit.
func buildTransferLog(block uint64, txIndex uint, to common.Address, amountMicro int64, txHash string) types.Log {
	toTopic := common.BytesToHash(to.Bytes())
	data := make([]byte, 32)
	big.NewInt(amountMicro).FillBytes(data)

	return types.Log{
		Address: common.HexToAddress(USDCContractSepolia),
		Topics: []common.Hash{
			transferEventSig,
			common.HexToHash("0x0000000000000000000000000000000000000001"), // from (sender)
			toTopic,
		},
		Data:        data,
		BlockNumber: block,
		TxHash:      common.HexToHash(txHash),
		TxIndex:     txIndex,
	}
}

// testDatabaseURL points at the isolated test database (falls back to
// skipping the suite when absent).
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("MONITOR_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgresql://ruvicode:ruvicode@localhost:15432/ruvicode_test?sslmode=disable"
}

// harness wires a Monitor against the isolated test database.
type harness struct {
	t          *testing.T
	mon        *Monitor
	pg         *store.PostgresStore
	addrs      map[string]string // hex address -> userID
	nextIndex  uint32            // derivation index for addUser
	mu         sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dbURL := testDatabaseURL(t)
	pg, err := store.NewPostgresStore(dbURL)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}

	mon := &Monitor{
		pg:         pg,
		usdc:       common.HexToAddress(USDCContractSepolia),
		minDeposit: 0.01,
	}

	// Fresh cursor + users for this run.
	mustExec(t, pg, `DELETE FROM monitor_cursor`)
	mustExec(t, pg, `DELETE FROM topups WHERE user_id LIKE 'mock-%'`)
	mustExec(t, pg, `DELETE FROM wallets WHERE user_id LIKE 'mock-%'`)
	mustExec(t, pg, `DELETE FROM deposit_addresses WHERE user_id LIKE 'mock-%'`)
	mustExec(t, pg, `DELETE FROM "user" WHERE id LIKE 'mock-%'`)

	return &harness{
		t:         t,
		mon:       mon,
		pg:        pg,
		addrs:     make(map[string]string),
		nextIndex: uint32(10000 + rand.Intn(10000)),
	}
}

func (h *harness) close() {
	h.pg.Close()
}

// addUser creates a user + wallet + deposit address derived at the next index.
func (h *harness) addUser(id string) common.Address {
	h.t.Helper()
	mustExec(h.t, h.pg,
		`INSERT INTO "user" (id, name, email, email_verified, created_at, updated_at)
		 VALUES ($1, $1, $1||'@mock.local', false, NOW(), NOW()) ON CONFLICT DO NOTHING`, id)
	mustExec(h.t, h.pg,
		`INSERT INTO wallets (user_id, balance, held, total_loaded, total_spent)
		 VALUES ($1, 0, 0, 0, 0) ON CONFLICT DO NOTHING`, id)

	hd, err := wallet.NewFromMnemonic(testMnemonic)
	if err != nil {
		h.t.Fatalf("mnemonic: %v", err)
	}
	// Derive in a high random band so parallel/previous runs never collide
	// on the same index (the harness DB is shared across test functions).
	h.mu.Lock()
	h.nextIndex++
	idx := h.nextIndex
	h.mu.Unlock()
	addr, _, err := hd.DeriveAddress(idx)
	if err != nil {
		h.t.Fatalf("derive: %v", err)
	}
	mustExec(h.t, h.pg,
		`INSERT INTO deposit_addresses (id, user_id, chain, address, derivation_index)
		 VALUES (gen_random_uuid()::text, $1, 84532, $2, $3)`, id, addr.Hex(), idx)

	h.addrs[addr.Hex()] = id
	return addr
}

// balance reads a user's wallet balance.
func (h *harness) balance(userID string) float64 {
	h.t.Helper()
	var bal float64
	if err := h.pg.Pool.QueryRow(context.Background(),
		`SELECT balance FROM wallets WHERE user_id = $1`, userID).Scan(&bal); err != nil {
		h.t.Fatalf("balance %s: %v", userID, err)
	}
	return bal
}

// topupCount counts credited USDC topups for a user.
func (h *harness) topupCount(userID string) int {
	h.t.Helper()
	var n int
	if err := h.pg.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM topups WHERE user_id = $1 AND method = 'usdc'`, userID).Scan(&n); err != nil {
		h.t.Fatalf("count %s: %v", userID, err)
	}
	return n
}

// cursor reads the persisted cursor.
func (h *harness) cursor() uint64 {
	h.t.Helper()
	var c uint64
	_ = h.pg.Pool.QueryRow(context.Background(),
		`SELECT last_processed_block FROM monitor_cursor WHERE id = 1`).Scan(&c)
	return c
}

func (h *harness) setCursor(block uint64) {
	h.t.Helper()
	mustExec(h.t, h.pg,
		`INSERT INTO monitor_cursor (id, last_processed_block) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET last_processed_block = $1`, block)
}

// process feeds logs through the credit path with a given scan head.
func (h *harness) process(scanHead uint64, logs ...types.Log) {
	for _, l := range logs {
		h.mon.processLog(context.Background(), l, h.addrs, scanHead)
	}
}

func mustExec(t *testing.T, pg *store.PostgresStore, q string, args ...any) {
	t.Helper()
	if _, err := pg.Pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
