package cliproxy

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedRoundRobinRoutingSelector(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "wrr"},
	})
	if state.strategy != "weighted-round-robin" {
		t.Fatalf("strategy = %q, want weighted-round-robin", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.WeightedRoundRobinSelector); !ok {
		t.Fatalf("selector type = %T, want *auth.WeightedRoundRobinSelector", newRoutingSelector(state))
	}
}

func TestSessionAffinityLoadAwareRoutingState(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinityLoadAware:     true,
			SessionAffinityLoadTolerance: 2,
		},
	})
	if state.loadAware {
		t.Fatal("load-aware enabled without session affinity")
	}

	state = normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinity:              true,
			SessionAffinityLoadAware:     true,
			SessionAffinityLoadTolerance: -3,
		},
	})
	if !state.loadAware || state.loadTolerance != 0 {
		t.Fatalf("state = %+v, want load-aware with tolerance 0", state)
	}
	if state == normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}}) {
		t.Fatal("enabling load-aware routing must change the routing state so the selector is rebuilt")
	}
}

func TestSessionAffinityRespectPriorityRoutingState(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinityRespectPriority: true,
		},
	})
	if state.sessionAffinityRespectPriority {
		t.Fatal("respect-priority enabled without session affinity")
	}

	state = normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			SessionAffinity:                true,
			SessionAffinityRespectPriority: true,
		},
	})
	if !state.sessionAffinityRespectPriority {
		t.Fatalf("state = %+v, want sessionAffinityRespectPriority true", state)
	}
	if state == normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{SessionAffinity: true}}) {
		t.Fatal("enabling session-affinity-respect-priority must change the routing state so the selector is rebuilt")
	}
}

func TestServiceRejectsInvalidCredentialWeightConfigCommit(t *testing.T) {
	originalCfg := &internalconfig.Config{}
	service := &Service{cfg: originalCfg}
	invalidWeight := internalconfig.MaxCredentialWeight + 1
	newCfg := &internalconfig.Config{
		VertexCompatAPIKey: []internalconfig.VertexCompatKey{{
			APIKey: "vertex-key",
			Weight: &invalidWeight,
		}},
	}

	if service.applyConfigUpdateWithAuthSynthesis(nil, newCfg, true) {
		t.Fatal("hot config application accepted an invalid credential weight")
	}
	if service.cfg != originalCfg {
		t.Fatal("invalid hot config replaced the active config")
	}
	if service.configSequence != 0 {
		t.Fatalf("config sequence = %d, want 0", service.configSequence)
	}
}

type trackingStoppableSelector struct {
	stopped bool
}

func (s *trackingStoppableSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (s *trackingStoppableSelector) Stop() {
	s.stopped = true
}

func TestApplyManagerConfigStopsReplacedServiceAffinitySelector(t *testing.T) {
	tracking := &trackingStoppableSelector{}
	service := &Service{
		coreManager: coreauth.NewManager(nil, tracking, nil),
	}

	newCfg := &internalconfig.Config{
		Routing: internalconfig.RoutingConfig{
			Strategy: "round-robin",
		},
	}
	commit := configCommit{cfg: newCfg, sequence: 1}
	if !service.applyManagerConfig(context.Background(), commit) {
		t.Fatal("applyManagerConfig failed")
	}

	if !tracking.stopped {
		t.Fatal("expected replaced selector to be stopped during routing config apply")
	}
}
