package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// quotaResetAt derives the next short-window quota reset for a credential from
// the passively observed quota signals (see QuotaState.Signals).
//
// Only the short rolling window is considered (Claude 5h, Codex primary, Devin
// daily): that is the window whose spend "comes back" soonest, so a credential
// whose short window is about to roll over is the cheapest one to burn now.
//
// Model-level signals win over credential-level ones when present for the
// requested model, since Claude reports per-model rate-limit headers.
// The second return value is false when no usable, still-future reset is known.
func quotaResetAt(auth *Auth, model string, now time.Time) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	if model != "" && len(auth.ModelStates) > 0 {
		modelKey := canonicalModelKey(model)
		for stateModel, state := range auth.ModelStates {
			if state == nil || canonicalModelKey(stateModel) != modelKey {
				continue
			}
			if reset, ok := quotaStateResetAt(auth.Provider, state.Quota, now); ok {
				return reset, true
			}
		}
	}
	return quotaStateResetAt(auth.Provider, auth.Quota, now)
}

func quotaStateResetAt(provider string, quota QuotaState, now time.Time) (time.Time, bool) {
	if len(quota.Signals) == 0 {
		return time.Time{}, false
	}
	var reset time.Time
	var ok bool
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic":
		reset, ok = parseQuotaResetTimestamp(quotaSignal(quota.Signals, "Anthropic-Ratelimit-Unified-5h-Reset"))
	case "codex", "openai":
		reset, ok = parseQuotaResetTimestamp(quotaSignal(quota.Signals, "X-Codex-Primary-Reset-At"))
		if !ok {
			reset, ok = quotaResetFromRelativeSeconds(quotaSignal(quota.Signals, "X-Codex-Primary-Reset-After-Seconds"), quota.ObservedAt)
		}
	case "devin":
		reset, ok = parseQuotaResetTimestamp(quotaSignal(quota.Signals, "daily_quota_reset_at"))
	default:
		return time.Time{}, false
	}
	if !ok || !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

// quotaSignal looks a header-style key up tolerating canonicalisation differences
// between HTTP observations and websocket/probe synthesised signals.
func quotaSignal(signals map[string]string, name string) string {
	if value, ok := signals[name]; ok {
		return value
	}
	canonical := http.CanonicalHeaderKey(name)
	if value, ok := signals[canonical]; ok {
		return value
	}
	lower := strings.ToLower(name)
	for key, value := range signals {
		if strings.ToLower(key) == lower {
			return value
		}
	}
	return ""
}

// parseQuotaResetTimestamp accepts unix seconds (optionally fractional), RFC3339 and
// HTTP-date encodings, mirroring the upstream header formats seen for Claude and Codex.
func parseQuotaResetTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil && sec > 0 {
		secInt := int64(sec)
		nsec := int64((sec - float64(secInt)) * 1e9)
		return time.Unix(secInt, nsec), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func quotaResetFromRelativeSeconds(raw string, observedAt time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || observedAt.IsZero() {
		return time.Time{}, false
	}
	sec, err := strconv.ParseFloat(raw, 64)
	if err != nil || sec < 0 {
		return time.Time{}, false
	}
	return observedAt.Add(time.Duration(sec * float64(time.Second))), true
}
