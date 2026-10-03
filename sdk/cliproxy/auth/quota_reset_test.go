package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestQuotaResetAt_ClaudeShortWindow(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	reset5h := now.Add(90 * time.Minute)
	reset7d := now.Add(5 * 24 * time.Hour)
	auth := &Auth{
		ID:       "claude-a",
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: now.Add(-time.Minute),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(reset5h.Unix(), 10),
				"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset7d.Unix(), 10),
			},
		},
	}

	got, ok := quotaResetAt(auth, "", now)
	if !ok {
		t.Fatalf("quotaResetAt() ok = false, want true")
	}
	if !got.Equal(reset5h) {
		t.Fatalf("quotaResetAt() = %v, want %v (5h window)", got, reset5h)
	}
}

func TestQuotaResetAt_CodexPrimaryPrefersAbsolute(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	observed := now.Add(-10 * time.Minute)
	resetAt := now.Add(2 * time.Hour)
	auth := &Auth{
		ID:       "codex-a",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: observed,
			Signals: map[string]string{
				"X-Codex-Primary-Reset-At":            strconv.FormatInt(resetAt.Unix(), 10),
				"X-Codex-Primary-Reset-After-Seconds": "999999",
				"X-Codex-Secondary-Reset-At":          strconv.FormatInt(now.Add(6*24*time.Hour).Unix(), 10),
			},
		},
	}

	got, ok := quotaResetAt(auth, "", now)
	if !ok {
		t.Fatalf("quotaResetAt() ok = false, want true")
	}
	if !got.Equal(resetAt) {
		t.Fatalf("quotaResetAt() = %v, want %v", got, resetAt)
	}
}

func TestQuotaResetAt_CodexPrimaryRelativeToObservedAt(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	observed := now.Add(-10 * time.Minute)
	auth := &Auth{
		ID:       "codex-b",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: observed,
			Signals: map[string]string{
				"X-Codex-Primary-Reset-After-Seconds": "3600",
			},
		},
	}

	got, ok := quotaResetAt(auth, "", now)
	if !ok {
		t.Fatalf("quotaResetAt() ok = false, want true")
	}
	if want := observed.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("quotaResetAt() = %v, want %v", got, want)
	}
}

func TestQuotaResetAt_DevinDailyRFC3339(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	daily := now.Add(3 * time.Hour)
	auth := &Auth{
		ID:       "devin-a",
		Provider: "devin",
		Quota: QuotaState{
			ObservedAt: now,
			Signals: map[string]string{
				"daily_quota_reset_at":  daily.Format(time.RFC3339),
				"weekly_quota_reset_at": now.Add(4 * 24 * time.Hour).Format(time.RFC3339),
			},
		},
	}

	got, ok := quotaResetAt(auth, "", now)
	if !ok {
		t.Fatalf("quotaResetAt() ok = false, want true")
	}
	if !got.Equal(daily) {
		t.Fatalf("quotaResetAt() = %v, want %v", got, daily)
	}
}

func TestQuotaResetAt_PastResetIsUnknown(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	auth := &Auth{
		ID:       "claude-stale",
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: now.Add(-6 * time.Hour),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
			},
		},
	}

	if _, ok := quotaResetAt(auth, "", now); ok {
		t.Fatalf("quotaResetAt() ok = true for a reset in the past, want false")
	}
}

func TestQuotaResetAt_NoSignals(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if _, ok := quotaResetAt(&Auth{ID: "antigravity-a", Provider: "antigravity"}, "", now); ok {
		t.Fatalf("quotaResetAt() ok = true without signals, want false")
	}
	if _, ok := quotaResetAt(nil, "", now); ok {
		t.Fatalf("quotaResetAt(nil) ok = true, want false")
	}
}

func TestQuotaResetAt_FallsBackToModelState(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	reset := now.Add(time.Hour)
	auth := &Auth{
		ID:       "claude-model",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4-5": {
				Quota: QuotaState{
					ObservedAt: now,
					Signals: map[string]string{
						"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(reset.Unix(), 10),
					},
				},
			},
		},
	}

	got, ok := quotaResetAt(auth, "claude-sonnet-4-5", now)
	if !ok {
		t.Fatalf("quotaResetAt() ok = false, want true")
	}
	if !got.Equal(reset) {
		t.Fatalf("quotaResetAt() = %v, want %v", got, reset)
	}
}
