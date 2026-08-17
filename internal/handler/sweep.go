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
	PreviewID         string          `json:"preview_id"`
	ExpiresAt         time.Time       `json:"expires_at"`
	Network           string          `json:"network"`
	ChainID           int64           `json:"chain_id"`
	UsdcContract      string          `json:"usdc_contract"`
	Treasury          string          `json:"treasury"`
	TreasuryUsdc      float64         `json:"treasury_usdc"`
	TreasuryEth       float64         `json:"treasury_eth"`
	Addresses         []sweepAddrInfo `json:"addresses"`
	TotalUSDC         float64         `json:"total_usdc"`
	TotalGasNeededEth float64         `json:"total_gas_needed_eth"`
	TreasuryCanFund   bool            `json:"treasury_can_fund"`
}

type sweepAddrInfo struct {
	Address         string  `json:"address"`
	UserID          string  `json:"user_id,omitempty"`
	UsdcBalance     float64 `json:"usdc_balance"`
	EthBalance      float64 `json:"eth_balance"`
	NeedsGas        bool    `json:"needs_gas"`
	EstimatedGasEth float64 `json:"estimated_gas_eth"`
	Action          string  `json:"action"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason,omitempty"`
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
		gasPerTx, gasErr := h.runner.EstimatedGasPerTxEth(r.Context())
		if gasErr != nil {
			slog.Error("sweep gas estimate failed", "error", gasErr)
			gasPerTx = 0
		}
		treasuryEth, ethErr := h.runner.EthBalance(r.Context(), h.treasury)
		if ethErr != nil {
			slog.Error("treasury eth balance failed", "error", ethErr)
			treasuryEth = 0
		}
		treasuryUsdc, usdcErr := h.runner.UsdcBalanceOf(r.Context(), h.treasury)
		if usdcErr != nil {
			slog.Error("treasury usdc balance failed", "error", usdcErr)
			treasuryUsdc = 0
		}
		var total float64
		var gasNeeded float64
		addresses := make([]sweepAddrInfo, 0, len(results))
		for _, res := range results {
			info := sweepAddrInfo{Address: res.Address, UserID: res.UserID, Action: "sweep", Status: "eligible"}
			if res.SweptUSDC > 0 {
				info.UsdcBalance = res.SweptUSDC
				total += res.SweptUSDC
				if eth, err := h.runner.EthBalance(r.Context(), res.Address); err == nil {
					info.EthBalance = eth
					if eth < gasPerTx {
						info.NeedsGas = true
						info.Action = "fund_gas_then_sweep"
						gasNeeded += gasPerTx - eth
					}
				} else {
					info.NeedsGas = true
					info.Action = "fund_gas_then_sweep"
					gasNeeded += gasPerTx
				}
				if res.SkippedMsg != "" {
					info.Reason = res.SkippedMsg
				}
				addresses = append(addresses, info)
			} else if res.SkippedMsg != "" {
				info.Status = "skipped"
				info.Reason = res.SkippedMsg
				addresses = append(addresses, info)
			}
		}
		id, auditErr := h.pg.WriteAuditLog(r.Context(), actor, "sweep_preview", operationID, "completed", map[string]any{"addresses": len(addresses), "total_usdc": total})
		if auditErr != nil {
			slog.Error("audit log failed", "error", auditErr)
			masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Audit logging failed")
			return
		}
		_ = id
		_ = json.NewEncoder(w).Encode(sweepDryRunResult{
			PreviewID: operationID, ExpiresAt: time.Now().Add(5 * time.Minute),
			Network: "base", ChainID: h.runner.ChainID(), UsdcContract: h.runner.UsdcContract(),
			Treasury: h.treasury, TreasuryUsdc: treasuryUsdc, TreasuryEth: treasuryEth,
			Addresses: addresses, TotalUSDC: total, TotalGasNeededEth: gasNeeded,
			TreasuryCanFund: treasuryEth >= gasNeeded,
		})
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
