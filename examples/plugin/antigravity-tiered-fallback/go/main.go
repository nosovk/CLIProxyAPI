package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginName             = "Antigravity Tiered Fallback"
	pluginVersion          = "0.1.0"
	defaultCooldownSeconds = 120

	headerDisableFallback  = "X-CPA-Disable-Tiered-Fallback"
	headerFallbackModel    = "X-CPA-Tiered-Fallback-Model"
	headerFallbackCooldown = "X-CPA-Tiered-Fallback-Cooldown-Seconds"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rpcLifecycleRequest struct {
	ConfigYAML         []byte `json:"config_yaml"`
	SchemaVersion      uint32 `json:"schema_version"`
	MultiAuthProviders bool   `json:"multi_auth_providers,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	RequestInterceptor               bool `json:"request_interceptor"`
	RequestInterceptorOmitBodyBefore bool `json:"request_interceptor_omit_body_before,omitempty"`
	RequestInterceptorOmitBodyAfter  bool `json:"request_interceptor_omit_body_after,omitempty"`
}

type rawPluginConfig struct {
	Enabled              *bool             `yaml:"enabled"`
	CooldownSeconds      *int              `yaml:"cooldown_seconds"`
	ForceImmediateTiered *bool             `yaml:"force_immediate_tiered"`
	FallbackMap          map[string]string `yaml:"fallback_map"`
}

type pluginConfig struct {
	Enabled              bool
	CooldownSeconds      int
	ForceImmediateTiered bool
	FallbackMap          map[string]string
}

var (
	configMu      sync.RWMutex
	currentConfig = defaultPluginConfig()
)

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:              true,
		CooldownSeconds:      defaultCooldownSeconds,
		ForceImmediateTiered: false,
		FallbackMap:          map[string]string{},
	}
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errCfg := applyLifecycleConfig(request); errCfg != nil {
			return errorEnvelope("invalid_config", errCfg.Error()), nil
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(struct{}{})
	case pluginabi.MethodRequestInterceptBefore:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return errorEnvelope("invalid_request", errUnmarshal.Error()), nil
			}
		}
		return okEnvelope(interceptAfterAuth(req, snapshotConfig()))
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "na",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Enable Antigravity non-tiered to tiered fallback headers.",
				},
				{
					Name:        "cooldown_seconds",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     defaultCooldownSeconds,
					Description: "Seconds to skip straight to the tiered model after a 429 on the non-tiered model (0 disables caching).",
				},
				{
					Name:        "force_immediate_tiered",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "Optional flag controlling immediate tiered routing behavior.",
				},
				{
					Name:        "fallback_map",
					Type:        pluginapi.ConfigFieldTypeObject,
					Default:     map[string]string{},
					Description: "Optional per-model overrides mapping base Antigravity models to tiered upstream models.",
				},
			},
		},
		Capabilities: registrationCapabilities{
			RequestInterceptor:               true,
			RequestInterceptorOmitBodyBefore: true,
			RequestInterceptorOmitBodyAfter:  true,
		},
	}
}

func applyLifecycleConfig(request []byte) error {
	var lifecycle rpcLifecycleRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &lifecycle); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	parsed, errParse := parseConfigYAML(lifecycle.ConfigYAML)
	if errParse != nil {
		return errParse
	}
	configMu.Lock()
	currentConfig = parsed
	configMu.Unlock()
	return nil
}

func snapshotConfig() pluginConfig {
	configMu.RLock()
	cfg := currentConfig
	configMu.RUnlock()
	return cfg
}

func parseConfigYAML(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	var rawCfg rawPluginConfig
	if errUnmarshal := yaml.Unmarshal(raw, &rawCfg); errUnmarshal != nil {
		return cfg, errUnmarshal
	}
	if rawCfg.Enabled != nil {
		cfg.Enabled = *rawCfg.Enabled
	}
	if rawCfg.CooldownSeconds != nil && *rawCfg.CooldownSeconds >= 0 {
		cfg.CooldownSeconds = *rawCfg.CooldownSeconds
	}
	if rawCfg.ForceImmediateTiered != nil {
		cfg.ForceImmediateTiered = *rawCfg.ForceImmediateTiered
	}
	if len(rawCfg.FallbackMap) > 0 {
		normalized := make(map[string]string, len(rawCfg.FallbackMap))
		for k, v := range rawCfg.FallbackMap {
			nk := strings.ToLower(strings.TrimSpace(k))
			nv := strings.TrimSpace(v)
			if nk != "" && nv != "" {
				normalized[nk] = nv
			}
		}
		cfg.FallbackMap = normalized
	}
	return cfg, nil
}

func lookupFallbackModel(fallbackMap map[string]string, model string) string {
	if len(fallbackMap) == 0 {
		return ""
	}
	norm := strings.ToLower(strings.TrimSpace(model))
	if norm == "" {
		return ""
	}
	if mapped, ok := fallbackMap[norm]; ok {
		return mapped
	}
	if idx := strings.IndexByte(norm, '('); idx > 0 {
		if mapped, ok := fallbackMap[strings.TrimSpace(norm[:idx])]; ok {
			return mapped
		}
	}
	return ""
}

func interceptAfterAuth(req pluginapi.RequestInterceptRequest, cfg pluginConfig) pluginapi.RequestInterceptResponse {
	if !strings.EqualFold(strings.TrimSpace(req.ToFormat), "antigravity") {
		return pluginapi.RequestInterceptResponse{}
	}
	headers := make(http.Header)
	if !cfg.Enabled {
		headers.Set(headerDisableFallback, "1")
		return pluginapi.RequestInterceptResponse{Headers: headers}
	}
	if cfg.CooldownSeconds >= 0 {
		headers.Set(headerFallbackCooldown, strconv.Itoa(cfg.CooldownSeconds))
	}
	if mapped := lookupFallbackModel(cfg.FallbackMap, req.Model); mapped != "" {
		headers.Set(headerFallbackModel, mapped)
	}
	return pluginapi.RequestInterceptResponse{Headers: headers}
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
