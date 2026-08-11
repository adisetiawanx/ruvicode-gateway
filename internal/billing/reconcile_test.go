package billing

import (
	"math"
	"testing"
)

func close(a, b, eps float64) bool { return math.Abs(a-b) < eps }

func TestEvaluateMarginOK(t *testing.T) {
	// ourRevenue 5.00, upstream 3.95 → margin 1.05, margin rate 21%.
	margin, pct, status := evaluateMargin(5.00, 3.95, 20.0)
	if status != ReconcileOK {
		t.Fatalf("expected ok, got %s", status)
	}
	if !close(margin, 1.05, 1e-9) {
		t.Fatalf("expected margin ~1.05, got %f", margin)
	}
	if !close(pct, 21.0, 1e-9) {
		t.Fatalf("expected margin pct ~21.0, got %f", pct)
	}
}

func TestEvaluateMarginNegative(t *testing.T) {
	// Charged less than upstream cost → actual loss.
	_, _, status := evaluateMargin(3.00, 4.00, 20.0)
	if status != ReconcileNegative {
		t.Fatalf("expected negative_margin, got %s", status)
	}
}

func TestEvaluateMarginBelowExpected(t *testing.T) {
	// Margin rate that is healthy but far below half the expected 20%.
	// revenue 10.00, upstream 9.50 → margin 0.50, rate 5% < 10% threshold.
	_, _, status := evaluateMargin(10.00, 9.50, 20.0)
	if status != ReconcileBelowExpected {
		t.Fatalf("expected margin_below_expected, got %s", status)
	}
}

func TestEvaluateMarginZeroUpstream(t *testing.T) {
	// No upstream cost (e.g. zero-cost window) and no loss → OK, no panic.
	_, _, status := evaluateMargin(0, 0, 20.0)
	if status != ReconcileOK {
		t.Fatalf("expected ok for empty window, got %s", status)
	}
}

func TestEvaluateMarginNoRevenue(t *testing.T) {
	// No revenue but an upstream cost on record → must be flagged negative.
	_, _, status := evaluateMargin(0, 1.00, 20.0)
	if status != ReconcileNegative {
		t.Fatalf("expected negative_margin for upstream cost with no revenue, got %s", status)
	}
}
