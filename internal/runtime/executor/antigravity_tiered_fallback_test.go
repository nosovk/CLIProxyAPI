package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func newTestAntigravityAuth(id, baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: id,
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": baseURL},
	}
}

func TestAntigravityTieredFallbackOn429StreamAndCache(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	var (
		mu             sync.Mutex
		seenModels     []string
		failFirstNonTi = true
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := ojson.GetBytesNoCopy(body, "model").String()

		mu.Lock()
		seenModels = append(seenModels, model)
		shouldFail := failFirstNonTi && model == "gemini-4-hydrogen-xhigh"
		if shouldFail {
			failFirstNonTi = false
		}
		mu.Unlock()

		if shouldFail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok-from-tiered\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-4-hydrogen-tiered-xhigh\",\"responseId\":\"resp-tiered-1\",\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}}\n\n")
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := newTestAntigravityAuth("auth-stream-fallback", server.URL)

	runStream := func() [][]byte {
		res, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-hydrogen-xhigh",
			Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: sdktranslator.FormatOpenAI,
			Stream:         true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		var chunks [][]byte
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
			chunks = append(chunks, chunk.Payload)
		}
		if len(chunks) == 0 {
			t.Fatal("expected stream chunks")
		}
		return chunks
	}

	chunks := runStream()
	mu.Lock()
	firstRunModels := append([]string(nil), seenModels...)
	seenModels = nil
	mu.Unlock()
	if len(firstRunModels) != 2 || firstRunModels[0] != "gemini-4-hydrogen-xhigh" || firstRunModels[1] != "gemini-4-hydrogen-tiered-xhigh" {
		t.Fatalf("first stream models = %v, want [gemini-4-hydrogen-xhigh gemini-4-hydrogen-tiered-xhigh]", firstRunModels)
	}
	for _, c := range chunks {
		if m := ojson.GetBytesNoCopy(c, "model").String(); m == "gemini-4-hydrogen-tiered-xhigh" {
			t.Fatalf("stream chunk exposed tiered model name %q instead of normalized name", m)
		}
	}

	// Second request within cache TTL skips non-tiered and goes straight to tiered.
	_ = runStream()
	mu.Lock()
	secondRunModels := append([]string(nil), seenModels...)
	mu.Unlock()
	if len(secondRunModels) != 1 || secondRunModels[0] != "gemini-4-hydrogen-tiered-xhigh" {
		t.Fatalf("second stream models = %v, want [gemini-4-hydrogen-tiered-xhigh]", secondRunModels)
	}
}

func TestAntigravityTieredFallbackOn429NonStream(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	var (
		mu         sync.Mutex
		seenModels []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := ojson.GetBytesNoCopy(body, "model").String()

		mu.Lock()
		seenModels = append(seenModels, model)
		mu.Unlock()

		if model == "gemini-3.8-flash-high" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"flash-tiered-ok"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-tiered","responseId":"resp-flash-1","usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":4,"totalTokenCount":12}}}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := newTestAntigravityAuth("auth-nonstream-fallback", server.URL)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty Execute payload")
	}
	if m := ojson.GetBytesNoCopy(resp.Payload, "model").String(); m == "gemini-3.8-flash-tiered" {
		t.Fatalf("non-stream response exposed tiered model name %q instead of normalized name", m)
	}

	mu.Lock()
	firstRunModels := append([]string(nil), seenModels...)
	seenModels = nil
	mu.Unlock()
	if len(firstRunModels) != 2 || firstRunModels[0] != "gemini-3.8-flash-high" || firstRunModels[1] != "gemini-3.8-flash-tiered" {
		t.Fatalf("first execute models = %v, want [gemini-3.8-flash-high gemini-3.8-flash-tiered]", firstRunModels)
	}

	// Second call hits cache directly.
	_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","messages":[{"role":"user","content":"hello again"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	mu.Lock()
	secondRunModels := append([]string(nil), seenModels...)
	mu.Unlock()
	if len(secondRunModels) != 1 || secondRunModels[0] != "gemini-3.8-flash-tiered" {
		t.Fatalf("second execute models = %v, want [gemini-3.8-flash-tiered]", secondRunModels)
	}
}

func TestAntigravityTieredFallbackDisabledByHeader(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	var (
		mu         sync.Mutex
		seenModels []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seenModels = append(seenModels, ojson.GetBytesNoCopy(body, "model").String())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := newTestAntigravityAuth("auth-disabled-fallback", server.URL)
	headers := make(http.Header)
	headers.Set(antigravityHeaderDisableTieredFallback, "1")

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen-xhigh",
		Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Headers:        headers,
	})
	if err == nil {
		t.Fatal("expected 429 error when tiered fallback is disabled")
	}
	mu.Lock()
	gotModels := append([]string(nil), seenModels...)
	mu.Unlock()
	if len(gotModels) != 1 || gotModels[0] != "gemini-4-hydrogen-xhigh" {
		t.Fatalf("seen models = %v, want [gemini-4-hydrogen-xhigh] only", gotModels)
	}
}

func TestAntigravityTieredFallbackRetryAfterAndKVPersistence(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	// 1. Dynamic TTL parsing from Google RetryInfo / quotaResetDelay and capping at 5h.
	body2h := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`)
	if got := antigravityEffectiveTieredFallbackTTL(120*time.Second, body2h, nil); got != 2*time.Hour {
		t.Fatalf("effective TTL for 7200s = %s, want 2h", got)
	}
	body10h := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"36000s"}]}}`)
	if got := antigravityEffectiveTieredFallbackTTL(120*time.Second, body10h, nil); got != 5*time.Hour {
		t.Fatalf("effective TTL for 36000s = %s, want capped 5h", got)
	}
	if got := antigravityEffectiveTieredFallbackTTL(0, body2h, nil); got != 0 {
		t.Fatalf("effective TTL when configured=0 = %s, want 0", got)
	}

	// 2. End-to-end KV persistence across memory reset and eviction on tiered 429.
	kv := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, kv, true, nil)

	var (
		mu             sync.Mutex
		seenModels     []string
		failTieredNext = false
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := ojson.GetBytesNoCopy(body, "model").String()

		mu.Lock()
		seenModels = append(seenModels, model)
		failTiered := failTieredNext
		mu.Unlock()

		if model == "gemini-4-hydrogen-xhigh" || failTiered {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Your quota will reset after 45m.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2700s"}]}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"modelVersion":"gemini-4-hydrogen-tiered-xhigh"}}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := newTestAntigravityAuth("auth-kv-persist", server.URL)

	// First request hits non-tiered 429 -> falls back to tiered 200 -> persists ~45m TTL to KV.
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen-xhigh",
		Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	kvKey := antigravityTieredFallbackKVKey(auth, "gemini-4-hydrogen-xhigh")
	if _, ok := kv.values[kvKey]; !ok {
		t.Fatalf("expected KV key %q to be persisted after fallback success", kvKey)
	}
	if kv.lastSetTTL < 45*time.Minute {
		t.Fatalf("kv TTL = %s, want >= 45m from Google retryDelay", kv.lastSetTTL)
	}

	// Simulate worker restart: wipe in-memory sync.Map, keep KV intact.
	resetAntigravityTieredFallbackCacheForTest()
	mu.Lock()
	seenModels = nil
	mu.Unlock()

	_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen-xhigh",
		Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","messages":[{"role":"user","content":"hi after restart"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("post-restart Execute() error = %v", err)
	}
	mu.Lock()
	afterRestartModels := append([]string(nil), seenModels...)
	seenModels = nil
	failTieredNext = true
	mu.Unlock()
	if len(afterRestartModels) != 1 || afterRestartModels[0] != "gemini-4-hydrogen-tiered-xhigh" {
		t.Fatalf("post-restart models = %v, want [gemini-4-hydrogen-tiered-xhigh] loaded from KV", afterRestartModels)
	}

	// If tiered itself returns 429, cached redirect is evicted from KV.
	_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen-xhigh",
		Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","messages":[{"role":"user","content":"tiered fails"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected error when tiered fails with 429")
	}
	if _, ok := kv.values[kvKey]; ok {
		t.Fatalf("expected KV key %q to be deleted after tiered returned 429", kvKey)
	}
}

func TestAntigravityTieredFallbackCapabilityAndModelStateChecks(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	now := time.Unix(1700000000, 0)
	authWithTiered := &cliproxyauth.Auth{
		ID: "auth-with-tiered",
		Metadata: map[string]any{
			"antigravity_capability_hints": map[string]any{
				"model_ids": []any{"gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh"},
			},
		},
	}
	if !antigravityAuthSupportsTieredModel(authWithTiered, "gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh", now) {
		t.Fatal("expected auth with tiered capability hint to allow fallback")
	}

	authNoTiered := &cliproxyauth.Auth{
		ID: "auth-without-tiered",
		Metadata: map[string]any{
			"antigravity_capability_hints": map[string]any{
				"model_ids": []any{"gemini-4-hydrogen-xhigh"},
			},
		},
	}
	if antigravityAuthSupportsTieredModel(authNoTiered, "gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh", now) {
		t.Fatal("expected auth missing tiered capability hint to disallow fallback")
	}

	authCooling := &cliproxyauth.Auth{
		ID: "auth-tiered-cooling",
		Metadata: map[string]any{
			"antigravity_capability_hints": map[string]any{
				"model_ids": []any{"gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh"},
			},
		},
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gemini-4-hydrogen-tiered-xhigh": {
				NextRetryAfter: now.Add(5 * time.Minute),
			},
		},
	}
	if antigravityAuthSupportsTieredModel(authCooling, "gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh", now) {
		t.Fatal("expected active ModelStates NextRetryAfter on tiered model to disallow fallback")
	}

	// Verify executor skips fallback completely when capability hints lack tiered model.
	var (
		mu         sync.Mutex
		seenModels []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seenModels = append(seenModels, ojson.GetBytesNoCopy(body, "model").String())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	authMissingTieredExec := newTestAntigravityAuth("auth-exec-no-tiered", server.URL)
	authMissingTieredExec.Metadata["antigravity_capability_hints"] = map[string]any{
		"model_ids": []any{"gemini-4-hydrogen-xhigh"},
	}
	_, err := exec.Execute(context.Background(), authMissingTieredExec, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen-xhigh",
		Payload: []byte(`{"model":"gemini-4-hydrogen-xhigh","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err == nil {
		t.Fatal("expected 429 error when account capability hints omit tiered model")
	}
	mu.Lock()
	gotModels := append([]string(nil), seenModels...)
	mu.Unlock()
	if len(gotModels) != 1 || gotModels[0] != "gemini-4-hydrogen-xhigh" {
		t.Fatalf("seen models = %v, want [gemini-4-hydrogen-xhigh] only", gotModels)
	}
}

func TestAntigravityTieredFallbackHeadersAndNormalization(t *testing.T) {
	resetAntigravityTieredFallbackCacheForTest()
	t.Cleanup(resetAntigravityTieredFallbackCacheForTest)

	h := make(http.Header)
	h.Set(antigravityHeaderTieredFallbackModel, "custom-tiered-model")
	h.Set(antigravityHeaderTieredFallbackCooldownSeconds, "45")
	model, ttl, ok := antigravityResolveTieredFallback("gemini-4-hydrogen-xhigh", h)
	if !ok || model != "custom-tiered-model" || ttl != 45*time.Second {
		t.Fatalf("resolve with headers = (%q, %s, %v), want (custom-tiered-model, 45s, true)", model, ttl, ok)
	}

	hDisable := make(http.Header)
	hDisable.Set(antigravityHeaderDisableTieredFallback, "1")
	if _, _, ok := antigravityResolveTieredFallback("gemini-4-hydrogen-xhigh", hDisable); ok {
		t.Fatal("expected disable header to suppress fallback")
	}

	cases := []struct {
		name      string
		requested string
		input     string
		wantPath  string
		wantVal   string
	}{
		{
			name:      "hydrogen-tiered-xhigh wrapped",
			requested: "gemini-4-hydrogen-xhigh",
			input:     `{"response":{"modelVersion":"gemini-4-hydrogen-tiered-xhigh"}}`,
			wantPath:  "response.modelVersion",
			wantVal:   "gemini-4-hydrogen",
		},
		{
			name:      "hydrogen-tiered-high unwrapped",
			requested: "gemini-4-hydrogen-high",
			input:     `{"modelVersion":"gemini-4-hydrogen-tiered-high"}`,
			wantPath:  "modelVersion",
			wantVal:   "gemini-4-hydrogen",
		},
		{
			name:      "flash-tiered normalized for flash-high",
			requested: "gemini-3.8-flash-high",
			input:     `{"response":{"modelVersion":"gemini-3.8-flash-tiered"}}`,
			wantPath:  "response.modelVersion",
			wantVal:   "gemini-3.8-flash",
		},
		{
			name:      "flash-tiered preserved when explicitly requested",
			requested: "gemini-3.8-flash-tiered",
			input:     `{"response":{"modelVersion":"gemini-3.8-flash-tiered"}}`,
			wantPath:  "response.modelVersion",
			wantVal:   "gemini-3.8-flash-tiered",
		},
		{
			name:      "argon-trial normalized",
			requested: "gemini-4-argon-high",
			input:     `{"response":{"modelVersion":"gemini-4-argon-trial"}}`,
			wantPath:  "response.modelVersion",
			wantVal:   "gemini-4-argon",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := antigravityNormalizeResponseModelVersion([]byte(tc.input), tc.requested, "")
			if got := ojson.GetBytesNoCopy(out, tc.wantPath).String(); got != tc.wantVal {
				t.Fatalf("got %q = %q, want %q", tc.wantPath, got, tc.wantVal)
			}
		})
	}

	auth := &cliproxyauth.Auth{ID: "auth-ttl-test"}
	now := time.Unix(1700000000, 0)
	if antigravityShouldUseTieredImmediately(context.Background(), auth, "gemini-4-hydrogen-xhigh", now) {
		t.Fatal("expected cache miss initially")
	}
	antigravityRecordTieredFallback(context.Background(), auth, "gemini-4-hydrogen-xhigh", "gemini-4-hydrogen-tiered-xhigh", 120*time.Second, now)
	if !antigravityShouldUseTieredImmediately(context.Background(), auth, "gemini-4-hydrogen-xhigh", now.Add(60*time.Second)) {
		t.Fatal("expected cache hit within TTL")
	}
	if antigravityShouldUseTieredImmediately(context.Background(), auth, "gemini-4-hydrogen-xhigh", now.Add(120*time.Second)) {
		t.Fatal("expected cache expiration at TTL boundary")
	}
}
