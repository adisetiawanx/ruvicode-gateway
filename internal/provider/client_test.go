package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeServer returns an httptest server that mimics the single upstream and
// records the last request body so tests can assert stream_options injection.
func startFakeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]interface{})) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
		}
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClientChatCompletionNonStreaming(t *testing.T) {
	srv := startFakeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("expected bearer auth, got %q", got)
		}
		if r.Header.Get("User-Agent") != "Ruvicode-Gateway/1.0" {
			t.Errorf("unexpected User-Agent %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("X-RateLimit-Remaining", "1190")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","usage":{"prompt_tokens":10,"completion_tokens":20}}`))
	})

	client := NewClient(srv.URL, []string{"inf_test"})
	result, err := client.ChatCompletion(context.Background(), &ChatRequest{
		Model:    "claude-opus-4.7",
		Messages: []Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	if result.Usage == nil || result.Usage.PromptTokens != 10 || result.Usage.CompletionTokens != 20 {
		t.Fatalf("expected usage 10/20, got %+v", result.Usage)
	}
	if len(result.Body) == 0 {
		t.Fatal("expected non-empty body")
	}
}

func TestClientInjectsStreamOptions(t *testing.T) {
	var captured map[string]interface{}
	srv := startFakeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		captured = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: [DONE]\n\n"))
	})

	client := NewClient(srv.URL, []string{"inf_test"})
	result, err := client.ChatCompletion(context.Background(), &ChatRequest{
		Model:  "glm-5.2",
		Stream: true,
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	defer result.Stream.Close()

	so, ok := captured["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatalf("stream_options not present in request: %v", captured)
	}
	if so["include_usage"] != true {
		t.Fatalf("expected include_usage true, got %v", so["include_usage"])
	}
	if result.Stream == nil {
		t.Fatal("expected a stream for streaming request")
	}
}

func TestClientStreamingReturnsBody(t *testing.T) {
	srv := startFakeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		w.Write([]byte("data: {\"id\":\"1\"}\n\n"))
		fl.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
	})

	client := NewClient(srv.URL, []string{"inf_test"})
	result, err := client.ChatCompletion(context.Background(), &ChatRequest{Model: "gpt-5.4", Stream: true})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	defer result.Stream.Close()

	raw, _ := io.ReadAll(result.Stream)
	text := string(raw)
	if !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("expected [DONE] in stream, got %q", text)
	}
}

func TestClientReturnsProviderError(t *testing.T) {
	srv := startFakeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"some internal detail"}}`))
	})

	client := NewClient(srv.URL, []string{"inf_test"})
	_, err := client.ChatCompletion(context.Background(), &ChatRequest{Model: "claude-opus-4.7"})
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
	if pe.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected status 402, got %d", pe.StatusCode)
	}
	if !strings.Contains(pe.RawBody, "internal detail") {
		t.Fatalf("expected raw body captured, got %q", pe.RawBody)
	}
}

func TestClientHealthCheck(t *testing.T) {
	srv := startFakeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	})

	client := NewClient(srv.URL, []string{"inf_test"})
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck error: %v", err)
	}
}

// DeepSeek-style responses carry completion_tokens_details as an object plus
// a provider cost block. The parser must tolerate both.
func TestParseUsageDeepSeekStyle(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1","object":"chat.completion","model":"deepseek-v4-flash",
		"usage":{"prompt_tokens":5,"completion_tokens":171,"total_tokens":176,
		         "prompt_tokens_details":{"cached_tokens":0},
		         "completion_tokens_details":{"reasoning_tokens":120}},
		"cost":{"usd":0.000047715,"diem":0}
	}`)

	usage, cost := parseUsage(body)
	if usage == nil {
		t.Fatal("expected usage parsed despite completion_tokens_details object")
	}
	if usage.PromptTokens != 5 || usage.CompletionTokens != 171 {
		t.Fatalf("expected usage 5/171, got %d/%d", usage.PromptTokens, usage.CompletionTokens)
	}
	if usage.ReasoningTokens != 120 {
		t.Fatalf("expected reasoning 120, got %d", usage.ReasoningTokens)
	}
	if cost != 0.000047715 {
		t.Fatalf("expected upstream cost 0.000047715, got %f", cost)
	}
}

func TestParseUsagePlainOpenAI(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	usage, cost := parseUsage(body)
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 20 {
		t.Fatalf("expected usage 10/20, got %+v", usage)
	}
	if cost != 0 {
		t.Fatalf("expected zero cost, got %f", cost)
	}
}
