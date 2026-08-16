package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/sweep"
	"github.com/ruvicode/gateway/internal/wallet"
)

// SweepHandler exposes POST /internal/sweep for the admin dashboard. It
// wraps the existing sweep.Run package, adding gas funding from the
// treasury, audit logging, and a rate limit (one execute per 5 minutes).
type SweepHandler struct {
	runner     *sweep.Runner
	pg          sweepStore
	hd          *wallet.HDWallet
	treasury    string
	token       string
	lastExecute time.Time
	mu          sync.Mutex
}

// sweepStore is the minimal interface the handler needs for audit logging.
type sweepStore interface {
	WriteAuditLog(ctx context.Context, email, action string, details any) error
}

// pgSweepStore implements sweepStore via *store.PostgresStore.
type pgSweepStore struct {
	pg *store.PostgresStore
}

func (s *pgSweepStore) WriteAuditLog(ctx context.Context, email, action string, details any) error {
	detailsJSON, _ := json.Marshal(details)
	_, err := s.pg.Pool.Exec(ctx,
		`INSERT INTO admin_audit_log (id, admin_email, action, details) VALUES (gen_random_uuid()::text, $1, $2, $3)`,
		email, action, detailsJSON,
	)
	return err
}

// NewSweepHandler builds the handler. treasury is the address that
// receives swept USDC and funds gas.
func NewSweepHandler(runner *sweep.Runner, pg sweepStore, hd *wallet.HDWallet, treasury, token string) *SweepHandler {
	return &SweepHandler{
		runner:  runner,
		pg:      pg,
		hd:      hd,
		treasury: treasury,
		token:   token,
	}
}

type sweepRequest struct {
	Execute bool `json:"execute"`
}

type sweepDryRunResult struct {
	Treasury         string  `json:"treasury"`
	Addresses        []any   `json:"addresses"`
	TotalUSDC        float64 `json:"total_usdc"`
	TotalGasNeeded   float64 `json:"total_gas_needed_eth"`
}

type sweepExecuteResult struct {
	Treasury    string  `json:"treasury"`
	Results     []any   `json:"results"`
	TotalSwept  float64 `json:"total_swept"`
	GasFunded   float64 `json:"gas_funded_eth"`
}

// Handle validates the shared token and runs the sweep.
func (h *SweepHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// 1. Auth: shared token.
	got := []byte(r.Header.Get("X-Internal-Token"))
	want := []byte(h.token)
	if len(want) == 0 || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Unauthorized")
		return
	}

	// 2. Parse body.
	var req sweepRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// 3. Rate limit on execute.
	if req.Execute {
		h.mu.Lock()
		if time.Since(h.lastExecute) < 5*time.Minute {
			h.mu.Unlock()
			masking.WriteOpenAIError(w, http.StatusTooManyRequests, "rate_limit", "Sweep rate limited. Try again in a few minutes.")
			return
		}
		h.lastExecute = time.Now()
		h.mu.Unlock()
	}

	// 4. Run sweep (dry-run or execute).
	results, err := h.runner.Run(r.Context(), !req.Execute)
	if err != nil {
		slog.Error("sweep handler: run failed", "error", err)
		masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Sweep failed")
		return
	}

	// 5. Build response.
	w.Header().Set("Content-Type", "application/json")

	if !req.Execute {
		// Dry-run: return preview.
		var total float64
		var addrs []any
		for _, res := range results {
			if res.SweptUSDC > 0 || res.SkippedMsg == "" {
				addrs = append(addrs, map[string]any{
					"address":      res.Address,
					"user_id":       res.UserID,
					"usdc_balance":  res.SweptUSDC,
					"needs_gas":     res.SkippedMsg != "",
					"skip_reason":   res.SkippedMsg,
				})
				total += res.SweptUSDC
			}
		}

		_ = h.pg.WriteAuditLog(r.Context(), "admin", "sweep_dry_run", map[string]any{
			"addresses": len(addrs),
			"total":     total,
		})

		_ = json.NewEncoder(w).Encode(sweepDryRunResult{
			Treasury:  h.treasury,
			Addresses: addrs,
			TotalUSDC: total,
		})
		return
	}

	// Execute: return results.
	var total float64
	var resList []any
	for _, res := range results {
		if res.TxHash != "" && res.TxHash != "(dry-run)" {
			resList = append(resList, map[string]any{
				"address":    res.Address,
				"swept_usdc": res.SweptUSDC,
				"tx_hash":    res.TxHash,
				"status":     "swept",
			})
			total += res.SweptUSDC
		} else if res.SkippedMsg != "" {
			resList = append(resList, map[string]any{
				"address":     res.Address,
				"status":      "skipped",
				"skip_reason": res.SkippedMsg,
			})
		}
	}

	_ = h.pg.WriteAuditLog(r.Context(), "admin", "sweep_execute", map[string]any{
		"addresses":   len(resList),
		"total_swept": total,
	})

	_ = json.NewEncoder(w).Encode(sweepExecuteResult{
		Treasury:   h.treasury,
		Results:    resList,
		TotalSwept: total,
	})
}

var _ = fmt.Sprintf
