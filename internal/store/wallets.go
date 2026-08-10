package store

import (
	"context"
)

// GetWalletBalance returns the user's canonical balance from Postgres.
func (s *PostgresStore) GetWalletBalance(ctx context.Context, userID string) (float64, error) {
	var balance float64
	err := s.Pool.QueryRow(ctx,
		"SELECT balance FROM wallets WHERE user_id = $1", userID).Scan(&balance)
	return balance, err
}

// GetWalletBalanceAndHeld returns the balance and held amounts together.
func (s *PostgresStore) GetWalletBalanceAndHeld(ctx context.Context, userID string) (balance, held float64, err error) {
	err = s.Pool.QueryRow(ctx,
		"SELECT balance, held FROM wallets WHERE user_id = $1", userID).Scan(&balance, &held)
	return
}

// CreditWallet adds funds to a user's wallet (top-up flow). It is a separate
// atomic operation from the gateway's deduction.
func (s *PostgresStore) CreditWallet(ctx context.Context, userID string, amount float64) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE wallets
		SET balance = balance + $1,
		    total_loaded = total_loaded + $1,
		    updated_at = NOW()
		WHERE user_id = $2
	`, amount, userID)
	return err
}

// EnsureWallet creates a zero-balance wallet row for a user if missing.
func (s *PostgresStore) EnsureWallet(ctx context.Context, userID string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO wallets (user_id, balance, held, total_loaded, total_spent)
		VALUES ($1, 0, 0, 0, 0)
		ON CONFLICT (user_id) DO NOTHING
	`, userID)
	return err
}
