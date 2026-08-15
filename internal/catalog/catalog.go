package catalog

// AllowedModels is the curated catalog enforced across the gateway, the
// pricing engine, and the public API. Only these slugs are synced as
// active, listed by /v1/models, and resolvable by chat requests.
//
// Keep in sync with ruvicode-web/src/lib/models/catalog.ts (single source
// shared via deployment; the web copy also carries display metadata).
var AllowedModels = map[string]struct{}{
	"claude-opus-5":          {},
	"claude-opus-4.7":        {},
	"claude-opus-4.8":        {},
	"claude-opus-4.6":        {},
	"claude-opus-4.5":        {},
	"claude-sonnet-5":        {},
	"claude-sonnet-4.5":      {},
	"claude-haiku-4.5":       {},
	"claude-fable-5":         {},
	"gemini-3-5-flash":       {},
	"gemini-3.1-pro-preview": {},
	"deepseek-v4-flash":      {},
	"deepseek-v4-flash-0731": {},
	"deepseek-v4-pro":        {},
	"glm-5.1":                {},
	"glm-5.2":                {},
	"grok-4.5":               {},
	"gpt-5.6-sol":            {},
	"gpt-5.6-sol-pro":        {},
	"gpt-5.6-terra":          {},
	"gpt-5.6-terra-pro":      {},
	"gpt-5.5":                {},
	"gpt-5.4":                {},
	"gpt-5.4-mini":           {},
	"kimi-k3":                {},
	"kimi-k2.5":              {},
	"kimi-k2.6":              {},
	"kimi-k2.7-code":         {},
	"minimax-m2.5":           {},
	"minimax-m2.7":           {},
}

// IsAllowed reports whether the model slug is part of the curated catalog.
func IsAllowed(model string) bool {
	_, ok := AllowedModels[model]
	return ok
}
