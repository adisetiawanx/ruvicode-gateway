package monitor

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ─── E2E mock test suite: deposit edge cases against the isolated DB ───
//
// Each test builds synthetic on-chain Transfer logs (exactly the shape
// Base emits) and drives them through processLog, the real credit path.
// The database is ruvicode_test — never production.

// TestFiveUsersDepositSimultaneously: 5 users, 5 deposits in the same block,
// same cycle. All must credit exactly once.
func TestFiveUsersDepositSimultaneously(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	users := []string{}
	addrs := []common.Address{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("mock-user-%d", i)
		h.addUser(id)
		users = append(users, id)
		addrs = append(addrs, common.HexToAddress(addrOf(h, id)))
	}

	logs := []types.Log{}
	for i := 0; i < 5; i++ {
		logs = append(logs, buildTransferLog(
			1000, uint(i), addrs[i], 5_000_000, // $5 each
			fmt.Sprintf("0x%064x", 0xa1120000+i),
		))
	}

	// Scan head well past confirmations.
	h.process(1100, logs...)

	for i, uid := range users {
		if bal := h.balance(uid); bal != 5.0 {
			t.Errorf("user %d balance = %v, want 5.0", i, bal)
		}
		if n := h.topupCount(uid); n != 1 {
			t.Errorf("user %d topups = %d, want 1", i, n)
		}
	}
}

// TestDepositWhileServerDown: deposit lands at block 1000 but the monitor
// is "down" until block 3000. When it comes back with a cursor before the
// deposit, the deposit must still credit.
func TestDepositWhileServerDown(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-down-user")
	addr := common.HexToAddress(addrOf(h, "mock-down-user"))

	// Deposit happened while "down".
	deposit := buildTransferLog(1000, 0, addr, 25_000_000, "0x00000000000000000000000000000000000000000000000000000000a110006")

	// Server restarts: cursor is still at 999 (last block before downtime).
	h.setCursor(999)

	// First cycle after restart scans from 1000; head is now 3000.
	h.process(3000, deposit)

	if bal := h.balance("mock-down-user"); bal != 25.0 {
		t.Errorf("balance after downtime = %v, want 25.0", bal)
	}
}

// TestTwoDepositsSameUserDifferentTx: one user, two separate transactions
// (like topup twice). Both must credit; two topup rows.
func TestTwoDepositsSameUserDifferentTx(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-twice-user")
	addr := common.HexToAddress(addrOf(h, "mock-twice-user"))

	h.process(1100,
		buildTransferLog(1000, 0, addr, 10_000_000, "0x00000000000000000000000000000000000000000000000000000000a110007"),
		buildTransferLog(1005, 0, addr, 7_500_000, "0x00000000000000000000000000000000000000000000000000000000a110008"),
	)

	if bal := h.balance("mock-twice-user"); bal != 17.5 {
		t.Errorf("balance = %v, want 17.5", bal)
	}
	if n := h.topupCount("mock-twice-user"); n != 2 {
		t.Errorf("topups = %d, want 2", n)
	}
}

// TestTwoUsersSameAmountConcurrentBlock: two users send the exact same
// amount in the same block. Addresses (not amounts) attribute deposits —
// both must credit correctly.
func TestTwoUsersSameAmountConcurrentBlock(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-same-a")
	h.addUser("mock-same-b")
	addrA := common.HexToAddress(addrOf(h, "mock-same-a"))
	addrB := common.HexToAddress(addrOf(h, "mock-same-b"))

	h.process(1100,
		buildTransferLog(1000, 0, addrA, 9_990_000, "0x00000000000000000000000000000000000000000000000000000000a110009"),
		buildTransferLog(1000, 1, addrB, 9_990_000, "0x00000000000000000000000000000000000000000000000000000000a11000a"),
	)

	if bal := h.balance("mock-same-a"); bal != 9.99 {
		t.Errorf("user A balance = %v, want 9.99", bal)
	}
	if bal := h.balance("mock-same-b"); bal != 9.99 {
		t.Errorf("user B balance = %v, want 9.99", bal)
	}
}

// TestDoubleProcessingSameLog: the same log delivered twice (restart, RPC
// replay, overlapping scan). Idempotency must hold — one credit only.
func TestDoubleProcessingSameLog(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-dup-user")
	addr := common.HexToAddress(addrOf(h, "mock-dup-user"))

	log := buildTransferLog(1000, 0, addr, 12_000_000, "0x00000000000000000000000000000000000000000000000000000000a11000b")

	// Process 3 times: normal, restart replay, manual re-run.
	h.process(1100, log)
	h.process(1200, log)
	h.process(1300, log)

	if bal := h.balance("mock-dup-user"); bal != 12.0 {
		t.Errorf("balance after 3 replays = %v, want 12.0", bal)
	}
	if n := h.topupCount("mock-dup-user"); n != 1 {
		t.Errorf("topups after replays = %d, want 1", n)
	}
}

// TestDepositSeenTooEarly: log observed at 2 confirmations must NOT credit
// yet (reorg window), then credits once confirmations arrive.
func TestDepositSeenTooEarly(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-early-user")
	addr := common.HexToAddress(addrOf(h, "mock-early-user"))

	log := buildTransferLog(1000, 0, addr, 3_330_000, "0x00000000000000000000000000000000000000000000000000000000a11000c")

	h.process(1002, log) // 2 confirmations — must NOT credit
	if bal := h.balance("mock-early-user"); bal != 0 {
		t.Errorf("balance at 2 conf = %v, want 0", bal)
	}

	h.process(1003, log) // 3 confirmations — credits now
	if bal := h.balance("mock-early-user"); bal != 3.33 {
		t.Errorf("balance at 3 conf = %v, want 3.33", bal)
	}
}

// TestMicroDepositIgnored: below min deposit ($0.01 here) — no credit, no row.
func TestMicroDepositIgnored(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-micro-user")
	addr := common.HexToAddress(addrOf(h, "mock-micro-user"))

	h.process(1100,
		buildTransferLog(1000, 0, addr, 1_000, "0x00000000000000000000000000000000000000000000000000000000a11000d"),   // $0.001
		buildTransferLog(1001, 0, addr, 9_999, "0x00000000000000000000000000000000000000000000000000000000a11000e"),   // $0.009999
	)

	if bal := h.balance("mock-micro-user"); bal != 0 {
		t.Errorf("balance = %v, want 0", bal)
	}
	if n := h.topupCount("mock-micro-user"); n != 0 {
		t.Errorf("topups = %d, want 0", n)
	}
}

// TestDepositToUnknownAddress: transfer to an address we do not watch —
// ignored silently (not our user).
func TestDepositToUnknownAddress(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	h.addUser("mock-known-user")
	stranger := common.HexToAddress("0x9999999999999999999999999999999999999999")

	h.process(1100,
		buildTransferLog(1000, 0, stranger, 50_000_000, "0x00000000000000000000000000000000000000000000000000000000a11000f"),
	)

	if bal := h.balance("mock-known-user"); bal != 0 {
		t.Errorf("known user balance = %v, want 0", bal)
	}
}

// TestWalletAutoCreated: user has a deposit address but no wallet row yet
// (edge: address created, wallet insert failed historically). The credit
// must still land via upsert.
func TestWalletAutoCreated(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	id := "mock-nowallet-user"
	h.addUser(id)
	// Simulate missing wallet row.
	mustExec(t, h.pg, `DELETE FROM wallets WHERE user_id = $1`, id)

	addr := common.HexToAddress(addrOf(h, id))
	h.process(1100, buildTransferLog(1000, 0, addr, 4_440_000, "0x00000000000000000000000000000000000000000000000000000000a110010"))

	if bal := h.balance(id); bal != 4.44 {
		t.Errorf("balance with auto-created wallet = %v, want 4.44", bal)
	}
}

// TestManyDepositsBurst: 20 deposits across 20 blocks in one catch-up scan
// after downtime — the realistic recovery scenario.
func TestManyDepositsBurst(t *testing.T) {
	h := newHarness(t)
	defer h.close()

	ids := []string{}
	var logs []types.Log
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("mock-burst-%d", i)
		h.addUser(id)
		ids = append(ids, id)
		addr := common.HexToAddress(addrOf(h, id))
		logs = append(logs, buildTransferLog(
			uint64(1000+i), 0, addr, 1_500_000, fmt.Sprintf("0x%064x", 0xa1110000+i),
		))
	}

	h.setCursor(999)
	h.process(1200, logs...)

	for i, id := range ids {
		if bal := h.balance(id); bal != 1.5 {
			t.Errorf("burst user %d balance = %v, want 1.5", i, bal)
		}
	}
}

// addrOf returns the hex deposit address for a user in the harness map.
func addrOf(h *harness, userID string) string {
	h.t.Helper()
	for addr, id := range h.addrs {
		if id == userID {
			return addr
		}
	}
	h.t.Fatalf("no address for user %s", userID)
	return ""
}
