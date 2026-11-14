package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func BenchmarkCredentialLoadLeastLoaded(b *testing.B) {
	for _, count := range []int{4, 32, 128} {
		for _, penalized := range []bool{false, true} {
			b.Run(fmt.Sprintf("accounts=%d/penalized=%t", count, penalized), func(b *testing.B) {
				tracker := newCredentialLoadTracker()
				auths := make([]*Auth, count)
				for i := range auths {
					auths[i] = &Auth{ID: fmt.Sprint(i)}
					if penalized {
						tracker.observeResult(Result{AuthID: auths[i].ID, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if len(tracker.leastLoaded(auths, 0)) == 0 {
							b.Error("no candidates")
						}
					}
				})
			})
		}
	}
}

func TestCredentialLoadParallelAcquireRelease(t *testing.T) {
	tracker := newCredentialLoadTracker()
	const workers = 32
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range 100 {
				release := tracker.acquire("shared")
				tracker.observeResult(Result{AuthID: "shared", Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
				tracker.leastLoaded([]*Auth{{ID: "shared"}, {ID: "idle"}}, 0)
				release()
				release()
			}
		})
	}
	wg.Wait()
	if len(tracker.inFlight) != 0 {
		t.Fatalf("in-flight slots leaked: %v", tracker.inFlight)
	}
	if got := len(tracker.rateLimited["shared"]); got > credentialRateLimitMaxHits {
		t.Fatalf("rate-limit history is unbounded: %d", got)
	}
}

func TestCredentialLoadFilterPreservesOrderAndInput(t *testing.T) {
	tracker := newCredentialLoadTracker()
	now := time.Unix(1_800_000_000, 0)
	tracker.now = func() time.Time { return now }
	auths := []*Auth{{ID: "busy"}, {ID: "idle-b"}, {ID: "idle-a"}}
	release := tracker.acquire("busy")
	defer release()
	for _, tolerance := range []float64{-1, 0, 1} {
		got := tracker.leastLoaded(auths, tolerance)
		if tolerance < 1 {
			if len(got) != 2 || got[0] != auths[1] || got[1] != auths[2] {
				t.Fatalf("filter changed order: %v", got)
			}
		} else if len(got) != 3 {
			t.Fatalf("tolerance excluded comparable auth: %v", got)
		}
		if auths[0].ID != "busy" || auths[1].ID != "idle-b" || auths[2].ID != "idle-a" {
			t.Fatal("filter mutated the candidate slice")
		}
	}
}

func TestCredentialLoadLargePoolFallback(t *testing.T) {
	tracker := newCredentialLoadTracker()
	auths := make([]*Auth, 257)
	for i := range auths {
		auths[i] = &Auth{ID: fmt.Sprint(i)}
		if i != 256 {
			defer tracker.acquire(auths[i].ID)()
		}
	}
	got := tracker.leastLoaded(auths, 0)
	if len(got) != 1 || got[0] != auths[256] {
		t.Fatalf("large pool filter = %v, want final idle credential", got)
	}
}

func TestCredentialLoadReleasedAfterCanceledStreamDrain(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "canceled-load-stream", Provider: "codex"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
	close(remaining) // Executors must close their channel after cancellation.
	released := make(chan struct{})
	release := manager.credentialLoad.acquire(auth.ID)
	result := manager.wrapStreamResult(ctx, auth, "codex", "model", "model", nil,
		nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, 0,
		func() { release(); close(released) })
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled stream did not release its load without a consumer")
	}
	if _, ok := <-result.Chunks; ok {
		t.Fatal("canceled stream emitted a chunk without a consumer")
	}
	if got := manager.credentialLoad.scores([]*Auth{auth})[0]; got != 0 {
		t.Fatalf("canceled stream leaked load: %v", got)
	}
}
