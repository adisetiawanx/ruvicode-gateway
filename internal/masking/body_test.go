package masking

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestStripCostFieldEndOfObject matches the real provider final chunk shape:
// cost is the last field before the closing brace.
func TestStripCostFieldEndOfObject(t *testing.T) {
	in := []byte(`data: {"id":"cmpl-1","usage":{"prompt_tokens":10,"completion_tokens":34},"cost":{"usd":0.00001073,"diem":0}}`)
	out := StripCostField(in)

	if strings.Contains(string(out), "cost") || strings.Contains(string(out), "usd") || strings.Contains(string(out), "diem") {
		t.Fatalf("cost object still present: %s", out)
	}
	if !strings.Contains(string(out), `"usage":{"prompt_tokens":10,"completion_tokens":34}`) {
		t.Fatalf("usage must survive stripping: %s", out)
	}
}

// TestStripCostFieldTrailingComma covers cost appearing mid-object.
func TestStripCostFieldTrailingComma(t *testing.T) {
	in := []byte(`{"cost":{"usd":1.5},"choices":[]}`)
	out := StripCostField(in)
	if string(out) != `{"choices":[]}` {
		t.Fatalf("expected {\"choices\":[]}, got %s", out)
	}
}

// TestStripCostFieldLeadingComma covers cost between other fields.
func TestStripCostFieldLeadingComma(t *testing.T) {
	in := []byte(`{"id":"x","cost":{"usd":1.5},"choices":[]}`)
	out := StripCostField(in)
	if string(out) != `{"id":"x","choices":[]}` {
		t.Fatalf("expected {\"id\":\"x\",\"choices\":[]}, got %s", out)
	}
}

// TestStripCostFieldWhitespaceTolerant covers flexible spacing.
func TestStripCostFieldWhitespaceTolerant(t *testing.T) {
	in := []byte(`{"usage":{},"cost" : { "usd" : 1 } }`)
	out := StripCostField(in)
	if strings.Contains(string(out), "cost") {
		t.Fatalf("cost with spaces not stripped: %s", out)
	}
}

// TestStripCostFieldNoCost leaves the body untouched.
func TestStripCostFieldNoCost(t *testing.T) {
	in := []byte(`{"id":"cmpl-mock","usage":{"prompt_tokens":24,"completion_tokens":36}}`)
	out := StripCostField(in)
	if string(out) != string(in) {
		t.Fatalf("expected unchanged bytes, got %s", out)
	}
}

// TestStripCostFieldStandalone sole-field object still yields valid JSON.
func TestStripCostFieldStandalone(t *testing.T) {
	in := []byte(`{"cost":{"usd":1}}`)
	out := StripCostField(in)
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped output is not valid JSON (%v): %s", err, out)
	}
}

// TestSanitizeResponseBody rewrites the routed model and strips the provider
// field from a streaming chunk (the shape observed live from the upstream).
func TestSanitizeResponseBody(t *testing.T) {
	in := []byte(`data: {"id":"gen-1","model":"deepseek/deepseek-v4-flash","provider":"DeepSeek","choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0,"is_byok":true,"cost_details":{"upstream_inference_cost":0.00002}}}`)
	out := SanitizeResponseBody(in, "deepseek-v4-flash")
	s := string(out)

	if strings.Contains(s, `"model":"deepseek/deepseek-v4-flash"`) {
		t.Fatalf("model prefix still present: %s", s)
	}
	if !strings.Contains(s, `"model":"deepseek-v4-flash"`) {
		t.Fatalf("model not rewritten: %s", s)
	}
	for _, leaked := range []string{`"provider"`, `"cost"`, `"cost_details"`, `"is_byok"`, "DeepSeek"} {
		if strings.Contains(s, leaked) {
			t.Fatalf("upstream field %q still present: %s", leaked, s)
		}
	}
	if !strings.Contains(s, `"delta":{"content":"hi"}`) {
		t.Fatalf("content must survive: %s", s)
	}
	if !strings.Contains(s, `"prompt_tokens":1,"completion_tokens":1`) {
		t.Fatalf("usage tokens must survive: %s", s)
	}
	// The scrubbed line must still be valid JSON after the data: prefix.
	if err := json.Unmarshal([]byte(strings.TrimPrefix(s, "data: ")), &struct{}{}); err != nil {
		t.Fatalf("sanitized line is invalid JSON (%v): %s", err, s)
	}
}

// TestSanitizeResponseBodyProviderTrailingComma covers provider mid-object.
func TestSanitizeResponseBodyProviderTrailingComma(t *testing.T) {
	in := []byte(`{"provider":"DeepSeek","id":"x","model":"deepseek/deepseek-v4-flash"}`)
	out := SanitizeResponseBody(in, "deepseek-v4-flash")
	s := string(out)

	if strings.Contains(s, "DeepSeek") || strings.Contains(s, `deepseek/`) {
		t.Fatalf("upstream identity leaked: %s", s)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("output is not valid JSON (%v): %s", err, s)
	}
}

// TestSanitizeResponseBodyNoLeakFields leaves a clean body untouched except
// the model rewrite.
func TestSanitizeResponseBodyClean(t *testing.T) {
	in := []byte(`{"id":"cmpl-1","model":"gpt-5.4","choices":[{"message":{"content":"ok"}}]}`)
	out := SanitizeResponseBody(in, "gpt-5.4")
	if string(out) != string(in) {
		t.Fatalf("expected unchanged body, got %s", out)
	}
}

// captureSlog runs fn with the default slog logger pointed at a buffer, then
// returns what was written. The previous default is restored afterwards so
// the rest of the suite logs normally.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// TestCheckBodyForLeaksWarnsOnForbiddenIdentifier verifies that a model body
// which happens to mention the provider is logged as a warning, matching the
// ADR-022 monitoring contract.
func TestCheckBodyForLeaksWarnsOnForbiddenIdentifier(t *testing.T) {
	out := captureSlog(t, func() {
		CheckBodyForLeaks([]byte(`Sure, here is info about surplusintelligence dot com`), "req-1")
	})
	if !strings.Contains(out, "response body leak warning") {
		t.Fatalf("expected leak warning logged, got: %s", out)
	}
	if !strings.Contains(out, "req-1") {
		t.Fatalf("expected request id in warning, got: %s", out)
	}
}

// TestCheckBodyForLeaksClean verifies a clean body produces no warning.
func TestCheckBodyForLeaksClean(t *testing.T) {
	out := captureSlog(t, func() {
		CheckBodyForLeaks([]byte(`Just a normal model answer with no leaks.`), "req-2")
	})
	if strings.Contains(out, "response body leak warning") {
		t.Fatalf("did not expect a warning for a clean body: %s", out)
	}
}