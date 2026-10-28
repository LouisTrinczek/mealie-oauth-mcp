// Command mealie-oauth-mcp is a remote MCP server for Mealie that
// authenticates users through Mealie's OIDC login.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run wires up and starts the server. It returns when ctx is cancelled or
// the server fails.
func run(ctx context.Context) error {
	_ = ctx
	return nil
}
