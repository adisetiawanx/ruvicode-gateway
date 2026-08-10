package masking

import (
	"log/slog"
	"strings"
)

// forbiddenIdentifiers are terms that should never appear in a response body.
// If found, we log a warning (the model output them, not our fault; monitoring only).
var forbiddenIdentifiers = []string{
	"surplusintelligence",
	"surplus intelligence",
	"surplus.ai",
}

// CheckBodyForLeaks scans a response body for forbidden identifiers.
// This is MONITORING only — we log a warning but never modify the body (which
// would break token/stream integrity).
func CheckBodyForLeaks(body []byte, requestID string) {
	lower := strings.ToLower(string(body))
	for _, id := range forbiddenIdentifiers {
		if strings.Contains(lower, id) {
			slog.Warn("response body leak warning",
				"request_id", requestID,
				"identifier", id,
				"note", "model output mentioned provider — monitoring only",
			)
			break
		}
	}
}
