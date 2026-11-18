package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityDefaultTieredFallbackCooldown = 120 * time.Second
	antigravityMaxTieredFallbackCooldown     = 5 * time.Hour
	antigravityNegativeKVCacheTTL            = 30 * time.Second

	antigravityHeaderDisableTieredFallback         = "X-CPA-Disable-Tiered-Fallback"
	antigravityHeaderTieredFallbackModel           = "X-CPA-Tiered-Fallback-Model"
	antigravityHeaderTieredFallbackCooldownSeconds = "X-CPA-Tiered-Fallback-Cooldown-Seconds"
)

var antigravityDefaultTieredFallbacks = map[string]string{
	"gemini-4-hydrogen-xhigh":  "gemini-4-hydrogen-tiered-xhigh",
	"gemini-4-hydrogen-high":   "gemini-4-hydrogen-tiered-high",
	"gemini-4-hydrogen":        "gemini-4-hydrogen-tiered-xhigh",
	"gemini-3.8-flash-high":    "gemini-3.8-flash-tiered",
	"gemini-3.8-flash":         "gemini-3.8-flash-tiered",
	"gemini-3.8-flash-low":     "gemini-3.8-flash-tiered",
	"gemini-3.8-flash-minimal": "gemini-3.8-flash-tiered",
}

var antigravityResponseModelAliases = map[string]string{
	"gemini-4-hydrogen-tiered-xhigh": "gemini-4-hydrogen",
	"gemini-4-hydrogen-tiered-high":  "gemini-4-hydrogen",
	"gemini-4-hydrogen-tiered":       "gemini-4-hydrogen",
	"gemini-3.8-flash-tiered":        "gemini-3.8-flash",
	"gemini-4-argon-trial":           "gemini-4-argon",
}

type antigravityTieredFallbackEntry struct {
	tieredModel    string
	expiresAt      time.Time
	kvCheckedUntil time.Time
}

var antigravityTieredFallbackCache sync.Map

func antigravityTieredFallbackKey(auth *cliproxyauth.Auth, baseModel string) string {
	if auth == nil {
		return ""
	}
	scope := strings.TrimSpace(antigravityTransportScope(auth))
	normModel := strings.ToLower(strings.TrimSpace(baseModel))
	if scope == "" || normModel == "" {
		return ""
	}
	return scope + "|" + normModel
}

func antigravityTieredFallbackKVKey(auth *cliproxyauth.Auth, baseModel string) string {
	if auth == nil {
		return ""
	}
	authID := strings.TrimSpace(auth.ID)
	normModel := strings.ToLower(strings.TrimSpace(baseModel))
	if authID == "" || normModel == "" {
		return ""
	}
	return "cpa:antigravity:tiered-fallback:" + authID + ":" + homekv.HashKeyPart(normModel)
}

// antigravityResolveTieredFallback resolves the tiered fallback upstream model and
// short-circuit cache TTL for baseModel, honoring override and disable headers.
func antigravityResolveTieredFallback(baseModel string, headers http.Header) (string, time.Duration, bool) {
	trimmedBase := strings.TrimSpace(baseModel)
	if trimmedBase == "" {
		return "", 0, false
	}
	if headers != nil {
		disableRaw := strings.TrimSpace(headers.Get(antigravityHeaderDisableTieredFallback))
		if disableRaw == "1" || strings.EqualFold(disableRaw, "true") {
			return "", 0, false
		}
	}

	var fallbackModel string
	if headers != nil {
		fallbackModel = strings.TrimSpace(headers.Get(antigravityHeaderTieredFallbackModel))
	}
	if fallbackModel == "" {
		fallbackModel = antigravityDefaultTieredFallbacks[strings.ToLower(trimmedBase)]
	}
	if fallbackModel == "" || strings.EqualFold(fallbackModel, trimmedBase) {
		return "", 0, false
	}

	cooldownTTL := antigravityDefaultTieredFallbackCooldown
	if headers != nil {
		if rawTTL := strings.TrimSpace(headers.Get(antigravityHeaderTieredFallbackCooldownSeconds)); rawTTL != "" {
			if secs, err := strconv.Atoi(rawTTL); err == nil && secs >= 0 {
				cooldownTTL = time.Duration(secs) * time.Second
			}
		}
	}
	return fallbackModel, cooldownTTL, true
}

// antigravityEffectiveTieredFallbackTTL computes the cache TTL for a triggered
// tiered fallback by extending configuredTTL with Google's retry delay (when larger),
// capped at antigravityMaxTieredFallbackCooldown. A non-positive configuredTTL disables caching.
func antigravityEffectiveTieredFallbackTTL(configuredTTL time.Duration, body []byte, headers http.Header) time.Duration {
	if configuredTTL <= 0 {
		return 0
	}
	ttl := configuredTTL
	if retryAfter, err := helps.ParseRetryDelay(body); err == nil && retryAfter != nil && *retryAfter > ttl {
		ttl = *retryAfter
	}
	if headers != nil {
		if secs, err := strconv.Atoi(strings.TrimSpace(headers.Get("Retry-After"))); err == nil && secs > 0 {
			if headerTTL := time.Duration(secs) * time.Second; headerTTL > ttl {
				ttl = headerTTL
			}
		}
	}
	if ttl > antigravityMaxTieredFallbackCooldown {
		ttl = antigravityMaxTieredFallbackCooldown
	}
	return ttl
}

// antigravityAuthSupportsTieredModel checks whether auth supports and is currently
// eligible to serve tieredModel based on ModelStates, excluded_models, persisted
// antigravity_capability_hints, and the global model registry.
func antigravityAuthSupportsTieredModel(auth *cliproxyauth.Auth, baseModel, tieredModel string, now time.Time) bool {
	if auth == nil || strings.TrimSpace(tieredModel) == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	normTiered := strings.TrimSpace(tieredModel)

	for stateModel, state := range auth.ModelStates {
		if state == nil || !strings.EqualFold(strings.TrimSpace(stateModel), normTiered) {
			continue
		}
		if state.Status == cliproxyauth.StatusDisabled {
			return false
		}
		if !state.NextRetryAfter.IsZero() && now.Before(state.NextRetryAfter) {
			return false
		}
		if state.Quota.Exceeded && !state.Quota.NextRecoverAt.IsZero() && now.Before(state.Quota.NextRecoverAt) {
			return false
		}
	}

	if auth.Metadata != nil {
		if rawExcluded, exists := auth.Metadata["excluded_models"]; exists && metadataContainsAntigravityModel(rawExcluded, normTiered) {
			return false
		}
		if rawHints, ok := auth.Metadata["antigravity_capability_hints"].(map[string]any); ok && rawHints != nil {
			if rawModels, hasModels := rawHints["model_ids"]; hasModels && rawModels != nil {
				if models := extractAntigravityModelStrings(rawModels); len(models) > 0 {
					for _, id := range models {
						if strings.EqualFold(id, normTiered) {
							return true
						}
					}
					return false
				}
			}
		}
	}

	if authID := strings.TrimSpace(auth.ID); authID != "" {
		if reg := registry.GetGlobalRegistry(); reg != nil && reg.ClientSupportsModel(authID, baseModel) {
			return reg.ClientSupportsModel(authID, normTiered)
		}
	}
	return true
}

func extractAntigravityModelStrings(raw any) []string {
	switch items := raw.(type) {
	case []string:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func metadataContainsAntigravityModel(raw any, target string) bool {
	for _, model := range extractAntigravityModelStrings(raw) {
		if strings.EqualFold(model, target) {
			return true
		}
	}
	return false
}

// antigravityShouldUseTieredImmediately reports whether recent 429 history for
// (auth, baseModel) warrants skipping straight to the tiered model, backed by
// both the in-process negative/positive cache and home KV storage.
func antigravityShouldUseTieredImmediately(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, now time.Time) bool {
	key := antigravityTieredFallbackKey(auth, baseModel)
	if key == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if raw, ok := antigravityTieredFallbackCache.Load(key); ok {
		entry, valid := raw.(antigravityTieredFallbackEntry)
		if !valid {
			antigravityTieredFallbackCache.Delete(key)
		} else if !entry.expiresAt.IsZero() {
			if now.Before(entry.expiresAt) {
				return true
			}
			antigravityTieredFallbackCache.Delete(key)
		} else if !entry.kvCheckedUntil.IsZero() && now.Before(entry.kvCheckedUntil) {
			return false
		}
	}

	kvKey := antigravityTieredFallbackKVKey(auth, baseModel)
	client, homeMode, errClient := currentAntigravityKVClient()
	if !homeMode || errClient != nil || client == nil || kvKey == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	raw, found, errGet := client.KVGet(ctx, kvKey)
	if errGet != nil {
		log.Debugf("antigravity executor: tiered fallback kv read error: %v", errGet)
		return false
	}
	if !found {
		antigravityTieredFallbackCache.Store(key, antigravityTieredFallbackEntry{
			kvCheckedUntil: now.Add(antigravityNegativeKVCacheTTL),
		})
		return false
	}

	nanoPart, tieredPart, _ := strings.Cut(strings.TrimSpace(string(raw)), "|")
	untilNano, errParse := strconv.ParseInt(strings.TrimSpace(nanoPart), 10, 64)
	if errParse != nil {
		return false
	}
	until := time.Unix(0, untilNano)
	if !now.Before(until) {
		return false
	}
	antigravityTieredFallbackCache.Store(key, antigravityTieredFallbackEntry{
		tieredModel:    strings.TrimSpace(tieredPart),
		expiresAt:      until,
		kvCheckedUntil: until,
	})
	return true
}

// antigravityRecordTieredFallback caches a temporary redirect from baseModel to
// tieredModel for (auth, baseModel) until now + ttl, in both memory and home KV.
func antigravityRecordTieredFallback(ctx context.Context, auth *cliproxyauth.Auth, baseModel, tieredModel string, ttl time.Duration, now time.Time) {
	trimmedTiered := strings.TrimSpace(tieredModel)
	if ttl <= 0 || trimmedTiered == "" {
		return
	}
	key := antigravityTieredFallbackKey(auth, baseModel)
	if key == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	expiresAt := now.Add(ttl)
	antigravityTieredFallbackCache.Store(key, antigravityTieredFallbackEntry{
		tieredModel:    trimmedTiered,
		expiresAt:      expiresAt,
		kvCheckedUntil: expiresAt,
	})

	kvKey := antigravityTieredFallbackKVKey(auth, baseModel)
	client, homeMode, errClient := currentAntigravityKVClient()
	if !homeMode || errClient != nil || client == nil || kvKey == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload := []byte(fmt.Sprintf("%d|%s", expiresAt.UnixNano(), trimmedTiered))
	if _, errSet := client.KVSet(ctx, kvKey, payload, homekv.KVSetOptions{EX: ttl + 5*time.Second}); errSet != nil {
		log.Debugf("antigravity executor: tiered fallback kv write error: %v", errSet)
	}
}

// antigravityClearTieredFallback evicts any cached redirect for (auth, baseModel)
// from both memory and home KV when the tiered fallback itself returns 429.
func antigravityClearTieredFallback(ctx context.Context, auth *cliproxyauth.Auth, baseModel string) {
	if key := antigravityTieredFallbackKey(auth, baseModel); key != "" {
		antigravityTieredFallbackCache.Delete(key)
	}
	kvKey := antigravityTieredFallbackKVKey(auth, baseModel)
	client, homeMode, errClient := currentAntigravityKVClient()
	if !homeMode || errClient != nil || client == nil || kvKey == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, errDel := client.KVDel(ctx, kvKey); errDel != nil {
		log.Debugf("antigravity executor: tiered fallback kv delete error: %v", errDel)
	}
}

func resetAntigravityTieredFallbackCacheForTest() {
	antigravityTieredFallbackCache.Range(func(k, _ any) bool {
		antigravityTieredFallbackCache.Delete(k)
		return true
	})
}

func antigravityNormalizeModelVersionValue(modelVersion, requestedBaseModel string) (string, bool) {
	normMV := strings.ToLower(strings.TrimSpace(modelVersion))
	if normMV == "" {
		return "", false
	}
	normReq := strings.ToLower(strings.TrimSpace(requestedBaseModel))
	if normReq != "" && normReq == normMV {
		return "", false
	}
	if strings.Contains(normMV, "-tiered") && strings.Contains(normReq, "-tiered") {
		return "", false
	}
	if strings.Contains(normMV, "-trial") && strings.Contains(normReq, "-trial") {
		return "", false
	}
	mapped, ok := antigravityResponseModelAliases[normMV]
	return mapped, ok
}

// antigravityNormalizeResponseModelVersion rewrites tiered or trial upstream
// modelVersion values back to their canonical base model names so response
// mismatch checks and downstream translators observe consistent model IDs.
func antigravityNormalizeResponseModelVersion(payload []byte, requestedBaseModel, actualUpstreamModel string) []byte {
	if len(payload) == 0 {
		return payload
	}
	_ = actualUpstreamModel

	out := payload
	if res := ojson.GetBytesNoCopy(out, "response.modelVersion"); res.Type == ojson.String {
		if mapped, ok := antigravityNormalizeModelVersionValue(res.String(), requestedBaseModel); ok {
			if updated, err := ojson.SetBytes(out, "response.modelVersion", mapped); err == nil {
				out = updated
			}
		}
	}
	if res := ojson.GetBytesNoCopy(out, "modelVersion"); res.Type == ojson.String {
		if mapped, ok := antigravityNormalizeModelVersionValue(res.String(), requestedBaseModel); ok {
			if updated, err := ojson.SetBytes(out, "modelVersion", mapped); err == nil {
				out = updated
			}
		}
	}
	return out
}

// selectAntigravityUpstreamModel resolves the initial upstream model for an Antigravity
// request, factoring in account capability hints, the tiered fallback cache, and short cooldowns.
func (e *AntigravityExecutor) selectAntigravityUpstreamModel(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	baseModel string,
	headers http.Header,
) (upstreamModel, tieredModel string, tieredCooldownTTL time.Duration, hasTieredFallback bool, err error) {
	now := time.Now()
	tieredModel, tieredCooldownTTL, hasTieredFallback = antigravityResolveTieredFallback(baseModel, headers)
	if hasTieredFallback && (!antigravityAuthSupportsTieredModel(auth, baseModel, tieredModel, now) ||
		cliproxyauth.CheckAntigravityTargetModel(ctx, auth, tieredModel, now, antigravityShouldBypassShortCooldown(ctx, e.cfg)) != nil) {
		log.Debugf("antigravity executor: auth %s does not support or blocked tiered model %s, skipping fallback", auth.ID, tieredModel)
		hasTieredFallback = false
		tieredModel = ""
		tieredCooldownTTL = 0
	}
	upstreamModel = baseModel
	if hasTieredFallback && antigravityShouldUseTieredImmediately(ctx, auth, baseModel, now) {
		log.Debugf("antigravity executor: auth %s using cached tiered fallback %s -> %s", auth.ID, baseModel, tieredModel)
		upstreamModel = tieredModel
	}
	if antigravityCoolingDisabled(auth, e.cfg) {
		return upstreamModel, tieredModel, tieredCooldownTTL, hasTieredFallback, nil
	}
	if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, upstreamModel, now); errCooldown != nil {
		return "", "", 0, false, homeKVUnavailableStatusErr(errCooldown)
	} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
		if hasTieredFallback && upstreamModel == baseModel {
			if tieredInCooldown, _, errTieredCooldown := antigravityIsInShortCooldownRequired(ctx, auth, tieredModel, now); errTieredCooldown != nil {
				return "", "", 0, false, homeKVUnavailableStatusErr(errTieredCooldown)
			} else if !tieredInCooldown {
				log.Debugf("antigravity executor: auth %s base model %s in short cooldown (%s remaining), switching to tiered fallback %s", auth.ID, baseModel, remaining, tieredModel)
				return tieredModel, tieredModel, tieredCooldownTTL, hasTieredFallback, nil
			}
		}
		log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, upstreamModel, remaining)
		d := remaining
		return "", "", 0, false, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
	}
	return upstreamModel, tieredModel, tieredCooldownTTL, hasTieredFallback, nil
}

// doWithTieredFallback builds and executes the Antigravity HTTP request and, when a
// non-tiered model immediately returns HTTP 429, retries on the tiered model within the
// same request, persisting the redirect only if the tiered fallback returns 2xx.
func (e *AntigravityExecutor) doWithTieredFallback(
	ctx context.Context,
	auth *cliproxyauth.Auth,
	prepared antigravityPreparedRequest,
	baseURL, traceOp string,
	stream bool,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	doc *ojson.Doc,
	httpClient *http.Client,
	reporter *helps.UsageReporter,
	to sdktranslator.Format,
) (*http.Response, string, []byte, error) {
	baseModel, upstreamModel, tieredModel := prepared.tieredBaseModel, prepared.model.ID, prepared.tieredModel
	hasTieredFallback := prepared.hasTieredFallback
	var fallbackDocBytes []byte
	if hasTieredFallback && upstreamModel == baseModel {
		fallbackDocBytes = bytes.Clone(doc.Bytes())
	}
	sessionIDs := helps.AntigravitySessionIDForAuth(antigravityAuthID(auth), opts.Metadata, req.Metadata)
	httpReq, requestPayload, errReq := e.buildRequestForModelDoc(ctx, auth, prepared.token, prepared.model.AntigravityModelSelection, doc, stream, opts.Alt, baseURL, sessionIDs)
	if errReq != nil {
		return nil, upstreamModel, nil, errReq
	}
	reporter.SetTranslatedRequestMetadata(requestPayload, to.String())

	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, traceOp).Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, upstreamModel, requestPayload, errDo
	}

	if httpResp.StatusCode == http.StatusTooManyRequests && hasTieredFallback && upstreamModel == baseModel {
		first429Body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64<<10))
		_ = httpResp.Body.Close()
		effectiveTTL := antigravityEffectiveTieredFallbackTTL(prepared.tieredCooldownTTL, first429Body, httpResp.Header)
		log.Warnf("antigravity executor: auth %s %s model %s returned 429, falling back immediately to tiered model %s (ttl=%s)", auth.ID, traceOp, baseModel, tieredModel, effectiveTTL)
		if errTarget := cliproxyauth.CheckAntigravityTargetModel(ctx, auth, tieredModel, time.Now(), antigravityShouldBypassShortCooldown(ctx, e.cfg)); errTarget != nil {
			return nil, upstreamModel, requestPayload, cliproxyauth.RejectAntigravityTarget(errTarget)
		}
		finalize := helps.NewPayloadFinalizerWithSourceModels(e.cfg, e.Identifier(), tieredModel, "antigravity", "request", prepared.originalTranslated, req, opts, baseModel)
		ctx = helps.WithPayloadFinalizer(ctx, finalize)
		httpReq, requestPayload, errReq = e.buildRequestDoc(ctx, auth, prepared.token, tieredModel, ojson.NewDoc(fallbackDocBytes), stream, opts.Alt, baseURL, sessionIDs)
		if errReq != nil {
			return nil, upstreamModel, requestPayload, errReq
		}
		upstreamModel = tieredModel
		cliproxyexecutor.SetUpstreamAttemptModel(ctx, upstreamModel)
		reporter.SetAttemptModel(upstreamModel)
		reporter.SetTranslatedRequestMetadata(requestPayload, to.String())
		httpResp, errDo = helps.WithAntigravityHTTPClientTrace(httpClient, auth, traceOp).Do(httpReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			return nil, upstreamModel, requestPayload, errDo
		}
		if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
			antigravityRecordTieredFallback(ctx, auth, baseModel, tieredModel, effectiveTTL, time.Now())
		}
	} else if httpResp.StatusCode == http.StatusTooManyRequests && hasTieredFallback && upstreamModel == tieredModel {
		antigravityClearTieredFallback(ctx, auth, baseModel)
	}

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	return httpResp, upstreamModel, requestPayload, nil
}
