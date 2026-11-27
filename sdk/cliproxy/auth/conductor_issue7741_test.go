package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIssue7741WrapStreamResultClosesOnCancellationDuringUpstreamStall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	t.Cleanup(func() { close(remaining) })

	resultReported := make(chan struct{})
	manager := NewManager(nil, nil, lifecycleTestHook{result: func() { close(resultReported) }})
	result := manager.wrapStreamResult(
		ctx,
		&Auth{ID: "issue7741-auth", Provider: "issue7741"},
		"issue7741",
		"issue7741-model",
		"issue7741-model",
		nil,
		nil,
		remaining,
		OAuthModelAliasResult{},
		false,
		cliproxyexecutor.Options{},
		1,
	)

	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.created"}`)}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("output closed before forwarding the first upstream chunk")
		}
		if len(chunk.Payload) == 0 {
			t.Fatal("forwarded chunk has an empty payload")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first upstream chunk")
	}

	cancel()

	select {
	case _, ok := <-result.Chunks:
		if ok {
			t.Fatal("output produced another chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("output did not close when context was canceled during upstream stall")
	}

	remaining <- cliproxyexecutor.StreamChunk{Err: &Error{
		Code:       ErrorCodeTransientTransport,
		Message:    "upstream failed after partial output",
		HTTPStatus: http.StatusBadGateway,
	}}
	select {
	case <-resultReported:
	case <-time.After(time.Second):
		t.Fatal("terminal upstream failure was not recorded after cancellation")
	}
}
