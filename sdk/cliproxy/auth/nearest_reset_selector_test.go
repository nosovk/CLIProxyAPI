package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthWithReset(id string, now time.Time, resetIn time.Duration) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: now,
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Reset": strconv.FormatInt(now.Add(resetIn).Unix(), 10),
			},
		},
	}
}

func TestNearestResetSelectorPick_PrefersSoonestReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	selector := &NearestResetSelector{}
	auths := []*Auth{
		claudeAuthWithReset("far", now, 4*time.Hour),
		claudeAuthWithReset("soon", now, 20*time.Minute),
		claudeAuthWithReset("mid", now, 2*time.Hour),
	}

	for i := 0; i < 3; i++ {
		got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil || got.ID != "soon" {
			t.Fatalf("Pick() #%d = %v, want %q", i, got, "soon")
		}
	}
}

func TestNearestResetSelectorPick_UnknownResetFirstRoundRobin(t *testing.T) {
	t.Parallel()

	now := time.Now()
	selector := &NearestResetSelector{}
	auths := []*Auth{
		claudeAuthWithReset("known-soon", now, 10*time.Minute),
		{ID: "fresh-b", Provider: "claude"},
		{ID: "fresh-a", Provider: "claude"},
	}

	// Credentials without an observed reset are probed first, rotating among themselves,
	// so the proxy learns their windows quickly. Known credentials come after.
	want := []string{"fresh-a", "fresh-b", "fresh-a", "fresh-b"}
	for i, wantID := range want {
		got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got == nil || got.ID != wantID {
			t.Fatalf("Pick() #%d = %v, want %q", i, got, wantID)
		}
	}
}

func TestNearestResetSelectorPick_TieBreaksByID(t *testing.T) {
	t.Parallel()

	now := time.Now()
	selector := &NearestResetSelector{}
	reset := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	mk := func(id string) *Auth {
		return &Auth{ID: id, Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": reset}}}
	}
	auths := []*Auth{mk("b"), mk("a"), mk("c")}

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "a" {
		t.Fatalf("Pick() = %v, want %q", got, "a")
	}
}

func TestNearestResetSelectorPick_RespectsPriorityAndCooldown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	selector := &NearestResetSelector{}
	soonLow := claudeAuthWithReset("soon-low", now, 5*time.Minute)
	soonLow.Attributes = map[string]string{"priority": "0"}
	farHigh := claudeAuthWithReset("far-high", now, 3*time.Hour)
	farHigh.Attributes = map[string]string{"priority": "10"}
	cooling := claudeAuthWithReset("cooling-high", now, time.Minute)
	cooling.Attributes = map[string]string{"priority": "10"}
	cooling.Unavailable = true
	cooling.NextRetryAfter = now.Add(10 * time.Minute)

	got, err := selector.Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, []*Auth{soonLow, farHigh, cooling})
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != "far-high" {
		t.Fatalf("Pick() = %v, want %q (highest available priority wins over sooner reset)", got, "far-high")
	}
}

func TestSchedulerPick_NearestReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	scheduler := newSchedulerForTest(
		&NearestResetSelector{},
		claudeAuthWithReset("far", now, 4*time.Hour),
		claudeAuthWithReset("soon", now, 20*time.Minute),
		claudeAuthWithReset("mid", now, 2*time.Hour),
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != "soon" {
			t.Fatalf("pickSingle() #%d = %v, want %q", index, got, "soon")
		}
	}

	// Excluding the soonest credential (retry round) falls through to the next nearest reset.
	tried := map[string]struct{}{"soon": {}}
	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, tried)
	if errPick != nil {
		t.Fatalf("pickSingle(tried) error = %v", errPick)
	}
	if got == nil || got.ID != "mid" {
		t.Fatalf("pickSingle(tried) = %v, want %q", got, "mid")
	}
}

func TestSchedulerPick_NearestResetUnknownFirst(t *testing.T) {
	t.Parallel()

	now := time.Now()
	scheduler := newSchedulerForTest(
		&NearestResetSelector{},
		claudeAuthWithReset("known", now, 10*time.Minute),
		&Auth{ID: "fresh-b", Provider: "claude"},
		&Auth{ID: "fresh-a", Provider: "claude"},
	)

	want := []string{"fresh-a", "fresh-b", "fresh-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil || got.ID != wantID {
			t.Fatalf("pickSingle() #%d = %v, want %q", index, got, wantID)
		}
	}
}

func TestSchedulerPick_NearestResetReordersAfterResultUpdate(t *testing.T) {
	t.Parallel()

	now := time.Now()
	a := claudeAuthWithReset("a", now, time.Hour)
	b := claudeAuthWithReset("b", now, 2*time.Hour)
	scheduler := newSchedulerForTest(&NearestResetSelector{}, a, b)

	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "a" {
		t.Fatalf("pickSingle() = %v, %v; want a", got, errPick)
	}

	// A fresh observation moves b's reset ahead of a's.
	updated := b.Clone()
	updated.Generation++
	updated.UpdatedAt = now.Add(time.Second)
	updated.Quota.ObservedAt = now.Add(time.Second)
	updated.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Reset"] = strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10)
	scheduler.upsertAuthResult(updated, nil, false)

	got, errPick = scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "b" {
		t.Fatalf("pickSingle() after update = %v, %v; want b", got, errPick)
	}
}

func TestSchedulerPick_NearestResetMixedProviders(t *testing.T) {
	t.Parallel()

	now := time.Now()
	codex := &Auth{
		ID:       "codex-soon",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: now,
			Signals:    map[string]string{"X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(5*time.Minute).Unix(), 10)},
		},
	}
	scheduler := newSchedulerForTest(
		&NearestResetSelector{},
		claudeAuthWithReset("claude-far", now, 3*time.Hour),
		codex,
	)

	got, providerKey, errPick := scheduler.pickMixed(context.Background(), []string{"claude", "codex"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "codex-soon" || providerKey != "codex" {
		t.Fatalf("pickMixed() = %v/%q, want codex-soon/codex", got, providerKey)
	}
}

func TestManager_InitializesSchedulerForNearestReset(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &NearestResetSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyNearestReset {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyNearestReset)
	}
	if !manager.useSchedulerFastPath() {
		t.Fatalf("useSchedulerFastPath() = false, want true for NearestResetSelector")
	}
}
