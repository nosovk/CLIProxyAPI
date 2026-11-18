package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHandleMethodRegisterReconfigureAndIntercept(t *testing.T) {
	regPayload, err := json.Marshal(rpcLifecycleRequest{
		SchemaVersion: pluginabi.SchemaVersion,
		ConfigYAML: []byte(`
enabled: true
cooldown_seconds: 90
force_immediate_tiered: false
fallback_map:
  gemini-4-hydrogen-xhigh: custom-hydrogen-tiered
`),
	})
	if err != nil {
		t.Fatalf("marshal register: %v", err)
	}

	rawResp, err := handleMethod(pluginabi.MethodPluginRegister, regPayload)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		t.Fatalf("unexpected register envelope: %s (err=%v)", rawResp, err)
	}
	var reg registration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("unmarshal registration: %v", err)
	}
	if reg.Metadata.Name == "" || reg.Metadata.Version == "" || reg.Metadata.Author == "" || reg.Metadata.GitHubRepository == "" {
		t.Fatalf("incomplete plugin metadata: %+v", reg.Metadata)
	}
	if !reg.Capabilities.RequestInterceptor || !reg.Capabilities.RequestInterceptorOmitBodyBefore || !reg.Capabilities.RequestInterceptorOmitBodyAfter {
		t.Fatalf("unexpected capabilities: %+v", reg.Capabilities)
	}

	// Non-antigravity target receives no headers.
	nonAgReq, _ := json.Marshal(pluginapi.RequestInterceptRequest{
		ToFormat: "openai",
		Model:    "gpt-5.5",
	})
	rawNonAg, err := handleMethod(pluginabi.MethodRequestInterceptAfter, nonAgReq)
	if err != nil {
		t.Fatalf("intercept non-antigravity: %v", err)
	}
	var nonAgEnv envelope
	_ = json.Unmarshal(rawNonAg, &nonAgEnv)
	var nonAgResp pluginapi.RequestInterceptResponse
	_ = json.Unmarshal(nonAgEnv.Result, &nonAgResp)
	if len(nonAgResp.Headers) != 0 {
		t.Fatalf("expected no headers for non-antigravity, got %v", nonAgResp.Headers)
	}

	// Antigravity request with configured map and cooldown.
	agReq, _ := json.Marshal(pluginapi.RequestInterceptRequest{
		ToFormat: "antigravity",
		Model:    "gemini-4-hydrogen-xhigh",
	})
	rawAg, err := handleMethod(pluginabi.MethodRequestInterceptAfter, agReq)
	if err != nil {
		t.Fatalf("intercept antigravity: %v", err)
	}
	var agEnv envelope
	_ = json.Unmarshal(rawAg, &agEnv)
	var agResp pluginapi.RequestInterceptResponse
	_ = json.Unmarshal(agEnv.Result, &agResp)
	if got := agResp.Headers.Get(headerFallbackCooldown); got != "90" {
		t.Fatalf("cooldown header = %q, want 90", got)
	}
	if got := agResp.Headers.Get(headerFallbackModel); got != "custom-hydrogen-tiered" {
		t.Fatalf("fallback model header = %q, want custom-hydrogen-tiered", got)
	}
	if got := agResp.Headers.Get(headerDisableFallback); got != "" {
		t.Fatalf("unexpected disable header %q", got)
	}

	// Reconfigure to disabled.
	disablePayload, _ := json.Marshal(rpcLifecycleRequest{
		SchemaVersion: pluginabi.SchemaVersion,
		ConfigYAML:    []byte("enabled: false\n"),
	})
	rawReconf, err := handleMethod(pluginabi.MethodPluginReconfigure, disablePayload)
	if err != nil {
		t.Fatalf("reconfigure failed: %v", err)
	}
	var reconfEnv envelope
	if err := json.Unmarshal(rawReconf, &reconfEnv); err != nil || !reconfEnv.OK {
		t.Fatalf("unexpected reconfigure envelope: %s", rawReconf)
	}

	rawAgDisabled, err := handleMethod(pluginabi.MethodRequestInterceptAfter, agReq)
	if err != nil {
		t.Fatalf("intercept disabled antigravity: %v", err)
	}
	var disEnv envelope
	_ = json.Unmarshal(rawAgDisabled, &disEnv)
	var disResp pluginapi.RequestInterceptResponse
	_ = json.Unmarshal(disEnv.Result, &disResp)
	if got := disResp.Headers.Get(headerDisableFallback); got != "1" {
		t.Fatalf("disable header = %q, want 1", got)
	}
}
