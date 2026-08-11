package masking

import (
	"encoding/json"
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