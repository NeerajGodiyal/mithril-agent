package mcpstdio

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// LimitToolCalls bounds asynchronous tool work for one local stdio server.
// Protocol setup and notifications do not consume a slot.
func LimitToolCalls(max int) mcpsdk.Middleware {
	active := make(chan struct{}, max)
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, request mcpsdk.Request) (mcpsdk.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			select {
			case active <- struct{}{}:
				defer func() { <-active }()
				return next(ctx, method, request)
			default:
				return nil, errors.New("MCP tool call concurrency limit reached")
			}
		}
	}
}
