package handler

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ruvicode/gateway/internal/masking"
	"github.com/ruvicode/gateway/internal/wallet"
)

// DepositAddressHandler exposes GET /internal/deposit-address for the
// dashboard: it returns (creating on first use) the caller's USDC deposit
// address on Base. Protected by the same shared token as the internal
// playground endpoint; the user id comes from the query string because
// the web server already authenticated the session.
type DepositAddressHandler struct {
	addresses *wallet.AddressManager
	token     string
}

// NewDepositAddressHandler builds the handler.
func NewDepositAddressHandler(addresses *wallet.AddressManager, token string) *DepositAddressHandler {
	return &DepositAddressHandler{addresses: addresses, token: token}
}

type depositAddressResponse struct {
	Address string `json:"address"`
	Chain   string `json:"chain"`
	Network string `json:"network"`
}

// Handle validates the shared token and returns the deposit address.
func (h *DepositAddressHandler) Handle(w http.ResponseWriter, r *http.Request) {
	got := []byte(r.Header.Get("X-Internal-Token"))
	want := []byte(h.token)
	if len(want) == 0 || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		masking.WriteOpenAIError(w, http.StatusUnauthorized, "authentication_error", "Unauthorized")
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		masking.WriteOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "user_id required")
		return
	}

	addr, err := h.addresses.GetOrCreateAddress(r.Context(), userID)
	if err != nil {
		slog.Error("get deposit address failed", "error", err)
		masking.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "Failed to get deposit address")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(depositAddressResponse{
		Address: addr,
		Chain:   "base",
		Network: "Base (Chain ID 8453)",
	})
}
