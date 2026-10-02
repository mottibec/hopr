package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"hopr/internal/handoff"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(handoff.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
