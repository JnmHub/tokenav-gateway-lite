package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

// Testable generation gate. Even an HTTP timeout must run cleanup once, but a
// failed shutdown or worker join can never authorize another application.
func shutdownBindingGeneration(ctx context.Context, server *http.Server, cleanup func(), join func(context.Context) error) error {
	if err := shutdownAndCleanup(ctx, server.Shutdown, cleanup); err != nil {
		return err
	}
	return join(ctx)
}

var bindingGenerationUnhealthy atomic.Bool

func markBindingGenerationUnhealthy()       { bindingGenerationUnhealthy.Store(true) }
func resetBindingGenerationHealth()         { bindingGenerationUnhealthy.Store(false) }
func bindingGenerationReadyToRestart() bool { return !bindingGenerationUnhealthy.Load() }
func joinBindingGeneration(context.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return gatewaylite.StopBindingWorkers(ctx)
}
