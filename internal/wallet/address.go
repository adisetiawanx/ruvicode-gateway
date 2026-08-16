package wallet

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ruvicode/gateway/internal/store"
)

// AddressManager creates and caches per-user deposit addresses.
type AddressManager struct {
	pg *store.PostgresStore
	hd *HDWallet
}

// NewAddressManager builds an AddressManager.
func NewAddressManager(pg *store.PostgresStore, hd *HDWallet) *AddressManager {
	return &AddressManager{pg: pg, hd: hd}
}

// GetOrCreateAddress returns the user's Base deposit address, deriving
// and persisting one at the next free index on first use.
//
// Concurrent first calls are safe: the derivation index is claimed with
// an INSERT, and a unique violation means another call raced us to the
// same index, so we retry with the next one.
func (m *AddressManager) GetOrCreateAddress(ctx context.Context, userID string) (string, error) {
	// Existing address?
	var address string
	err := m.pg.Pool.QueryRow(ctx,
		`SELECT address FROM deposit_addresses
		 WHERE user_id = $1 ORDER BY derivation_index ASC LIMIT 1`,
		userID,
	).Scan(&address)
	if err == nil {
		return address, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("query deposit address: %w", err)
	}

	// Derive at the next free index, retrying on races.
	for attempt := 0; attempt < 5; attempt++ {
		var maxIdx int
		err := m.pg.Pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(derivation_index), -1) FROM deposit_addresses`,
		).Scan(&maxIdx)
		if err != nil {
			return "", fmt.Errorf("query max index: %w", err)
		}
		nextIdx := uint32(maxIdx + 1)

		addr, _, err := m.hd.DeriveAddress(nextIdx)
		if err != nil {
			return "", fmt.Errorf("derive address: %w", err)
		}

		addressStr := addr.Hex()
		_, err = m.pg.Pool.Exec(ctx, `
			INSERT INTO deposit_addresses (id, user_id, chain, address, derivation_index)
			VALUES (gen_random_uuid()::text, $1, 8453, $2, $3)
			ON CONFLICT DO NOTHING
		`, userID, addressStr, nextIdx)
		if err != nil {
			return "", fmt.Errorf("insert deposit address: %w", err)
		}

		// Confirm it is ours (another user may have raced the same index).
		var owner string
		err = m.pg.Pool.QueryRow(ctx,
			`SELECT user_id FROM deposit_addresses WHERE address = $1`,
			addressStr,
		).Scan(&owner)
		if err != nil {
			return "", fmt.Errorf("verify deposit address: %w", err)
		}
		if owner == userID {
			return addressStr, nil
		}
		// Lost the race — loop and try the next index.
	}

	return "", fmt.Errorf("could not allocate a deposit address for user %s", userID)
}
