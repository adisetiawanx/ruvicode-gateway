package catalog

// ModelMeta carries the display metadata for an allowlisted model. Context
// and max output were verified against vendor docs (Aug 2026); the market
// feed exposes none of this.
type ModelMeta struct {
	DisplayName string
	Context     int // context window, tokens
	MaxOutput   int // max output tokens
}

// AllowedModels is the curated catalog enforced across the gateway, the
// pricing engine, and the public API. Only these slugs are synced as
// active, listed by /v1/models, and resolvable by chat requests.
//
// Keep in sync with ruvicode-web/src/lib/models/catalog.ts (single source
// shared via deployment; the web copy also carries display metadata).
var AllowedModels = map[string]ModelMeta{
	"claude-opus-5":          {DisplayName: "Claude Opus 5", Context: 1_000_000, MaxOutput: 128_000},
	"claude-opus-4.7":        {DisplayName: "Claude Opus 4.7", Context: 1_000_000, MaxOutput: 128_000},
	"claude-opus-4.8":        {DisplayName: "Claude Opus 4.8", Context: 1_000_000, MaxOutput: 128_000},
	"claude-opus-4.6":        {DisplayName: "Claude Opus 4.6", Context: 1_000_000, MaxOutput: 128_000},
	"claude-opus-4.5":        {DisplayName: "Claude Opus 4.5", Context: 200_000, MaxOutput: 64_000},
	"claude-sonnet-5":        {DisplayName: "Claude Sonnet 5", Context: 1_000_000, MaxOutput: 128_000},
	"claude-sonnet-4.5":      {DisplayName: "Claude Sonnet 4.5", Context: 200_000, MaxOutput: 64_000},
	"claude-haiku-4.5":       {DisplayName: "Claude Haiku 4.5", Context: 200_000, MaxOutput: 64_000},
	"claude-fable-5":         {DisplayName: "Claude Fable 5", Context: 1_000_000, MaxOutput: 128_000},
	"gemini-3-5-flash":       {DisplayName: "Gemini 3.5 Flash", Context: 1_048_576, MaxOutput: 64_000},
	"gemini-3.1-pro-preview": {DisplayName: "Gemini 3.1 Pro", Context: 1_048_576, MaxOutput: 64_000},
	"deepseek-v4-flash":      {DisplayName: "DeepSeek V4 Flash", Context: 1_048_576, MaxOutput: 384_000},
	"deepseek-v4-flash-0731": {DisplayName: "DeepSeek V4 Flash 0731", Context: 1_048_576, MaxOutput: 384_000},
	"deepseek-v4-pro":        {DisplayName: "DeepSeek V4 Pro", Context: 1_048_576, MaxOutput: 384_000},
	"glm-5.1":                {DisplayName: "GLM-5.1", Context: 200_000, MaxOutput: 128_000},
	"glm-5.2":                {DisplayName: "GLM-5.2", Context: 1_000_000, MaxOutput: 128_000},
	"grok-4.5":               {DisplayName: "Grok 4.5", Context: 500_000, MaxOutput: 128_000},
	"grok-4.3":               {DisplayName: "Grok 4.3", Context: 1_000_000, MaxOutput: 128_000},
	"gpt-5.6-sol":            {DisplayName: "GPT-5.6 Sol", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.6-sol-pro":        {DisplayName: "GPT-5.6 Sol Pro", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.6-terra":          {DisplayName: "GPT-5.6 Terra", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.6-terra-pro":      {DisplayName: "GPT-5.6 Terra Pro", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.6-luna":           {DisplayName: "GPT-5.6 Luna", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.6-luna-pro":       {DisplayName: "GPT-5.6 Luna Pro", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.5":                {DisplayName: "GPT-5.5", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.4":                {DisplayName: "GPT-5.4", Context: 1_050_000, MaxOutput: 128_000},
	"gpt-5.4-mini":           {DisplayName: "GPT-5.4 Mini", Context: 400_000, MaxOutput: 128_000},
	"kimi-k3":                {DisplayName: "Kimi K3", Context: 1_000_000, MaxOutput: 128_000},
	"kimi-k2.5":              {DisplayName: "Kimi K2.5", Context: 256_000, MaxOutput: 128_000},
	"kimi-k2.6":              {DisplayName: "Kimi K2.6", Context: 256_000, MaxOutput: 128_000},
	"kimi-k2.7-code":         {DisplayName: "Kimi K2.7 Code", Context: 256_000, MaxOutput: 128_000},
	"minimax-m2.5":           {DisplayName: "MiniMax M2.5", Context: 204_800, MaxOutput: 128_000},
	"minimax-m2.7":           {DisplayName: "MiniMax M2.7", Context: 204_800, MaxOutput: 128_000},
}

// IsAllowed reports whether the model slug is part of the curated catalog.
func IsAllowed(model string) bool {
	_, ok := AllowedModels[model]
	return ok
}

// Meta returns the display metadata for an allowlisted slug.
func Meta(model string) (ModelMeta, bool) {
	meta, ok := AllowedModels[model]
	return meta, ok
}
