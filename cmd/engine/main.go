// Command engine is the composition root. It reads the environment, opens the
// database, wires the Context Builder, the RCA Rule Engine and the gRPC
// transport together, and serves until it is asked to stop.
//
// Nothing else in the tree may wire modules to each other: every package below
// depends on interfaces, and this is the single place that decides which
// implementation satisfies them.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// The signal context is established before anything else so a SIGTERM
	// arriving during startup — a rollout replacing the pod while it is still
	// connecting to the database — cancels the work in progress instead of
	// being noticed only once the server is up.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		// Written to stderr, not through the JSON logger: the most likely
		// failure here is a configuration error that happens before the logger
		// exists, and an operator reading a crash wants one plain line.
		fmt.Fprintln(os.Stderr, "engine:", err)
		os.Exit(1)
	}
}
