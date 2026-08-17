package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/store"
	"github.com/ruvicode/gateway/internal/sweep"
	"github.com/ruvicode/gateway/internal/wallet"
)

var adminEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type SweepHandler struct {
	runner      *sweep.Runner
	pg          sweepStore
	hd          *wallet.HDWallet
	treasury    string
	token       string
	mu          sync.Mutex
	lastExecute time.Time
}

type sweepStore interface {
	WriteAuditLog(ctx context.Context, email, action, operationID, status string, details any) (string, error)
}

type PgSweepStore struct{ pg *store.PostgresStore }

func NewPgSweepStore(pg *store.PostgresStore) *PgSweepStore { return &PgSweepStore{pg: pg} }
func (s *PgSweepStore) WriteAuditLog(ctx context.Context, email, action, operationID, status string, details any) (string, error) {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return "", err
	}
	var id string
	err = s.pg.Pool.QueryRow(ctx, `INSERT INTO admin_audit_log (admin_email, action, operation_id, status, details) VALUES ($1, $2, $3, $4, $5) RETURNING id`, email, action, operationID, status, detailsJSON).Scan(&id)
	return id, err
}
func NewSweepHandler(runner *sweep.Runner, pg sweepStore, hd *wallet.HDWallet, treasury, token string) *SweepHandler {
	return &SweepHandler{runner: runner, pg: pg, hd: hd, treasury: treasury, token: token}
}

type sweepRequest struct {
	Execute      bool   `json:"execute"`
	PreviewID    string `json:"preview_id"`
	Confirmation string `json:"confirmation"`
}
type sweepDryRunResult struct {
	PreviewID      string    `json:"preview_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	Treasury       string    `json:"treasury"`
	Addresses      []any     `json:"addresses"`
	TotalUSDC      float64   `json:"total_usdc"`
	TotalGasNeeded float64   `json:"total_gas_needed_eth"`
}
type sweepExecuteResult struct {
	OperationID string  `json:"operation_id"`
	Status      string  `json:"status"`
	Treasury    string  `json:"treasury"`
	Results     []any   `json:"results"`
	TotalSwept  float64 `json:"total_swept"`
	GasFunded   float64 `json:"gas_funded_eth"`
	AuditID     string  `json:"audit_id"`
}

func (h *SweepHandler) Handle(w http.ResponseWriter, r *http.Request) {
	got, want := []byte(r.Header.Get("X-Internal-Token")), []byte(h.token)
	if len(want) == 0 || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Unauthorized")
		return
	}
	actor := strings.TrimSpace(r.Header.Get("X-Admin-Actor"))
	if !adminEmailPattern.MatchString(actor) {
		masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Unauthorized")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	}
	var req sweepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, context.Canceled) {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request")
		return
	}
	if req.Execute && (req.PreviewID == "" || req.Confirmation != "SWEEP") {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "A valid preview and confirmation are required")
		return
	}
	if req.Execute {
		h.mu.Lock()
		if time.Since(h.lastExecute) < 5*time.Minute {
			h.mu.Unlock()
			masking.WriteOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "Sweep rate limited")
			return
		}
		h.lastExecute = time.Now()
		h.mu.Unlock()
	}
	results, err := h.runner.Run(r.Context(), !req.Execute)
	if err != nil {
		slog.Error("sweep failed", "error", err)
		masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Sweep failed")
		return
	}
	operationID := fmt.Sprintf("sweep-%d", time.Now().UnixNano())
	w.Header().Set("Content-Type", "application/json")
	if !req.Execute {
		var total float64
		addresses := make([]any, 0, len(results))
		for _, res := range results {
			if res.SweptUSDC > 0 || res.SkippedMsg == "" {
				addresses = append(addresses, map[string]any{"address": res.Address, "user_id": res.UserID, "usdc_balance": res.SweptUSDC, "needs_gas": res.SkippedMsg != "", "action": "sweep", "status": "eligible", "reason": res.SkippedMsg})
				total += res.SweptUSDC
			}
		}
		id, auditErr := h.pg.WriteAuditLog(r.Context(), actor, "sweep_preview", operationID, "completed", map[string]any{"addresses": len(addresses), "total_usdc": total})
		if auditErr != nil {
			slog.Error("audit log failed", "error", auditErr)
			masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Audit logging failed")
			return
		}
		_ = id
		_ = json.NewEncoder(w).Encode(sweepDryRunResult{PreviewID: operationID, ExpiresAt: time.Now().Add(5 * time.Minute), Treasury: h.treasury, Addresses: addresses, TotalUSDC: total})
		return
	}
	var total float64
	list := make([]any, 0, len(results))
	status := "completed"
	for _, res := range results {
		if res.TxHash != "" && res.TxHash != "(dry-run)" {
			list = append(list, map[string]any{"address": res.Address, "swept_usdc": res.SweptUSDC, "tx_hash": res.TxHash, "status": "submitted"})
			total += res.SweptUSDC
		} else if res.SkippedMsg != "" {
			status = "partial"
			list = append(list, map[string]any{"address": res.Address, "status": "skipped", "skip_reason": res.SkippedMsg})
		}
	}
	id, auditErr := h.pg.WriteAuditLog(r.Context(), actor, "sweep_execute_"+status, operationID, status, map[string]any{"addresses": len(list), "total_swept": total})
	if auditErr != nil {
		slog.Error("audit log failed", "error", auditErr)
		masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Audit logging failed")
		return
	}
	_ = json.NewEncoder(w).Encode(sweepExecuteResult{OperationID: operationID, Status: status, Treasury: h.treasury, Results: list, TotalSwept: total, AuditID: id})
}
