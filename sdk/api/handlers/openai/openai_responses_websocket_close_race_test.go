package openai

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestResponsesWebsocketCleanupPreservesPendingDisconnectFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		err       error
		closeCode int
	}{
		{
			name: "quota error",
			err: websocketPinnedFailoverStatusError{
				status: http.StatusTooManyRequests,
				msg:    `{"error":{"type":"usage_limit_reached","message":"quota exceeded"}}`,
			},
		},
		{
			name:      "transport close",
			err:       &websocket.CloseError{Code: websocket.CloseMessageTooBig, Text: "message too big"},
			closeCode: websocket.CloseMessageTooBig,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clientRaw, serverRaw := net.Pipe()
				defer func() { _ = clientRaw.Close() }()
				defer func() { _ = serverRaw.Close() }()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				executor := &websocketUpstreamDisconnectExecutor{provider: "codex", subscribed: make(chan string, 1)}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(executor)
				h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
				done := make(chan struct{})
				go func() {
					defer close(done)
					req, errRead := http.ReadRequest(bufio.NewReader(serverRaw))
					if errRead != nil {
						t.Error(errRead)
						return
					}
					c, _ := gin.CreateTestContext(newHijackableWebsocketResponseWriter(serverRaw))
					c.Request = req.WithContext(ctx)
					h.ResponsesWebsocket(c)
				}()
				conn, _, errDial := websocket.NewClient(clientRaw, &url.URL{Scheme: "ws", Host: "example.com", Path: "/v1/responses"}, nil, 4096, 4096)
				if errDial != nil {
					t.Fatal(errDial)
				}
				defer func() { _ = conn.Close() }()
				// Avoid a close reply blocking on a server that is already closing.
				conn.SetCloseHandler(func(int, string) error { return nil })
				executor.TriggerDisconnect(<-executor.subscribed, tc.err)
				// The terminal writer owns closure and is blocked until the client reads.
				synctest.Wait()
				// Model the handler returning while the disconnect writer is still active.
				cancel()
				synctest.Wait()
				_, frame, errRead := conn.ReadMessage()
				if tc.closeCode != 0 {
					if !websocket.IsCloseError(errRead, tc.closeCode) {
						t.Fatalf("close = %v, want code %d", errRead, tc.closeCode)
					}
				} else {
					if errRead != nil || ojson.GetBytesNoCopy(frame, "status").Int() != http.StatusTooManyRequests {
						t.Fatalf("terminal frame = %s, err=%v; want 429", frame, errRead)
					}
					if _, duplicate, errClose := conn.ReadMessage(); errClose == nil {
						t.Fatalf("unexpected duplicate terminal frame: %s", duplicate)
					}
				}
				<-done
			})
		})
	}
}
