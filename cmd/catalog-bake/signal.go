package main

import (
	"context"
	"os"
	"os/signal"
)

// interruptContext cancels the bake on Ctrl-C so a retry wait returns
// promptly and no staged directory is published.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
