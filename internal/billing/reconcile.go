package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ReconcileStatus describes the outcome of a reconciliation run.
type ReconcileStatus string

const (
	// ReconcileOK means the logged margin is healthy.
	ReconcileOK ReconcileStatus = "ok"
	// ReconcileNegative means Ruvicode charged less than the upstream cost
	// (an actual loss on the window).
	ReconcileNegative ReconcileStatus = "negative_margin"
	// ReconcileBelowExpected means the margin rate is healthy but well under
	// the expected spread, which can signal pricing drift.
	ReconcileBelowExpected ReconcileStatus = "margin_below_expected"
)

// ReconcileResult is the outcome of a reconciliation run, exposed so tests and
// callers can inspect what happened without parsing log lines.
type ReconcileResult struct {
	Since         time.Time
	OurTotal      float64
	UpstreamTotal float64
	Margin        float64
	MarginPct     float64
	Status        ReconcileStatus
}

// evaluateMargin computes the margin and its status from the raw totals. It is
// a pure function so the reconciliation policy can be unit-tested.
//
// Margin is Ruvicode gross margin (user revenue minus upstream cost). The
// expected margin is the configured spread (20 percentage points default).
// The ADR flags a warning when the margin rate relative to revenue falls below
// half the expected spread, and an error when the margin is negative.
func evaluateMargin(ourTotal, upstreamTotal, expectedMarginPct float64) (margin, marginPct float64, status ReconcileStatus) {
	margin = ourTotal - upstreamTotal

	if ourTotal > 0 {
		marginPct = margin / ourTotal * 100
	}

	if margin < 0 {
		return margin, marginPct, ReconcileNegative
	}
	if upstreamTotal > 0 && marginPct < expectedMarginPct*0.5 {
		return margin, marginPct, ReconcileBelowExpected
	}
	return margin, marginPct, ReconcileOK
}

// Reconcile runs the reconciliation over the trailing hour. It is the safety
// net that compares Ruvicode's usage_records against the cost the provider
// reported to us. Any negative margin is an actual loss and is logged as an
// error; a margin rate far below the expected spread is logged as a warning.
func (e *Engine) Reconcile(ctx context.Context) (*ReconcileResult, error) {
	return e.ReconcileWindow(ctx, time.Now().Add(-1*time.Hour))
}

// ReconcileWindow runs the reconciliation over usage at or after since.
func (e *Engine) ReconcileWindow(ctx context.Context, since time.Time) (*ReconcileResult, error) {
	var ourTotal, upstreamTotal float64
	err := e.pg.Pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(cost), 0),
			COALESCE(SUM(upstream_cost), 0)
		FROM usage_records
		WHERE created_at >= $1 AND status = 'completed'
	`, since).Scan(&ourTotal, &upstreamTotal)
	if err != nil {
		return nil, fmt.Errorf("reconcile query failed: %w", err)
	}

	margin, marginPct, status := evaluateMargin(ourTotal, upstreamTotal, 20.0)
	res := &ReconcileResult{
		Since:         since,
		OurTotal:      ourTotal,
		UpstreamTotal: upstreamTotal,
		Margin:        margin,
		MarginPct:     marginPct,
		Status:        status,
	}

	slog.Info("reconciliation",
		"since", since.Format("15:04"),
		"our_revenue", round6(ourTotal),
		"upstream_cost", round6(upstreamTotal),
		"margin", round6(margin),
		"margin_pct", round2(marginPct),
		"status", status,
	)

	switch status {
	case ReconcileNegative:
		slog.Error("reconciliation: negative margin, losing money on this window",
			"our_revenue", round6(ourTotal),
			"upstream_cost", round6(upstreamTotal),
			"loss", round6(margin),
		)
	case ReconcileBelowExpected:
		slog.Warn("reconciliation: margin below expected",
			"expected_pct", 20.0,
			"actual_pct", round2(marginPct),
		)
	}

	return res, nil
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
