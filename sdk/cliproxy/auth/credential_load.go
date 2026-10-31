package auth

import (
	"math"
	"net/http"
	"sync"
	"time"
)

const (
	// credentialRateLimitPenalty is the load, in in-flight request equivalents,
	// that a fresh 429 adds to its credential. It decays linearly to zero over
	// credentialRateLimitWindow.
	credentialRateLimitPenalty = 2.0
	credentialRateLimitWindow  = 5 * time.Minute
	credentialRateLimitMaxHits = 8
)

// credentialLoadTracker counts upstream requests currently executing on each
// credential and remembers recent rate limits, so new session bindings can
// avoid credentials that are already busy.
type credentialLoadTracker struct {
	mu          sync.Mutex
	inFlight    map[string]int
	rateLimited map[string][]time.Time
	now         func() time.Time
}

func newCredentialLoadTracker() *credentialLoadTracker {
	return &credentialLoadTracker{
		inFlight:    make(map[string]int),
		rateLimited: make(map[string][]time.Time),
		now:         time.Now,
	}
}

// acquire marks one request as in flight on authID and returns a release
// function that is safe to call more than once.
func (t *credentialLoadTracker) acquire(authID string) func() {
	if t == nil || authID == "" {
		return func() {}
	}
	t.mu.Lock()
	t.inFlight[authID]++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			if t.inFlight[authID] <= 1 {
				delete(t.inFlight, authID)
			} else {
				t.inFlight[authID]--
			}
			t.mu.Unlock()
		})
	}
}

func (t *credentialLoadTracker) observeResult(result Result) {
	if t == nil || result.AuthID == "" || result.Success {
		return
	}
	if statusCodeFromResult(result.Error) != http.StatusTooManyRequests {
		return
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	hits := append(pruneRateLimitHits(t.rateLimited[result.AuthID], now), now)
	if len(hits) > credentialRateLimitMaxHits {
		hits = hits[len(hits)-credentialRateLimitMaxHits:]
	}
	t.rateLimited[result.AuthID] = hits
}

// scores returns the current load of each auth: in-flight requests plus the
// decayed rate-limit penalty.
func (t *credentialLoadTracker) scores(auths []*Auth) []float64 {
	scores := make([]float64, len(auths))
	if t == nil {
		return scores
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, auth := range auths {
		if auth == nil {
			continue
		}
		score := float64(t.inFlight[auth.ID])
		hits := pruneRateLimitHits(t.rateLimited[auth.ID], now)
		if len(hits) == 0 {
			delete(t.rateLimited, auth.ID)
		} else {
			t.rateLimited[auth.ID] = hits
		}
		for _, hit := range hits {
			score += credentialRateLimitPenalty * (1 - float64(now.Sub(hit))/float64(credentialRateLimitWindow))
		}
		scores[i] = score
	}
	return scores
}

// leastLoaded keeps the auths whose load is within tolerance of the least
// loaded one, preserving their order so the fallback selector still decides
// among comparable credentials.
func (t *credentialLoadTracker) leastLoaded(auths []*Auth, tolerance float64) []*Auth {
	if t == nil || len(auths) <= 1 {
		return auths
	}
	scores := t.scores(auths)
	minScore := math.Inf(1)
	for _, score := range scores {
		minScore = math.Min(minScore, score)
	}
	limit := minScore + math.Max(tolerance, 0)
	filtered := make([]*Auth, 0, len(auths))
	for i, auth := range auths {
		if scores[i] <= limit {
			filtered = append(filtered, auth)
		}
	}
	return filtered
}

type credentialLoadConsumer interface {
	setCredentialLoad(*credentialLoadTracker)
}

func attachCredentialLoad(selector Selector, tracker *credentialLoadTracker) {
	if consumer, ok := selector.(credentialLoadConsumer); ok && tracker != nil {
		consumer.setCredentialLoad(tracker)
	}
}

func pruneRateLimitHits(hits []time.Time, now time.Time) []time.Time {
	kept := hits[:0]
	for _, hit := range hits {
		if now.Sub(hit) < credentialRateLimitWindow {
			kept = append(kept, hit)
		}
	}
	return kept
}
