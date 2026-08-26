package mcpstdio

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLimitToolCallsRejectsExcessWork(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := LimitToolCalls(1)(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		close(entered)
		<-release
		return nil, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := handler(t.Context(), "tools/call", nil)
		done <- err
	}()
	<-entered
	if _, err := handler(t.Context(), "tools/call", nil); err == nil ||
		!strings.Contains(err.Error(), "concurrency limit") {
		t.Fatalf("excess call error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLimitToolCallsDoesNotLimitProtocolMessages(t *testing.T) {
	called := false
	handler := LimitToolCalls(1)(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
		called = true
		return nil, nil
	})
	if _, err := handler(t.Context(), "initialize", nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("protocol message was not passed through")
	}
}
