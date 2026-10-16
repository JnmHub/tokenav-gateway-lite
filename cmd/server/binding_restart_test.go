package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestBindingRestartRealHTTPShutdownThenCleanupAndJoin(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(200) })}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Close()
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
		close(finished)
	}()
	<-entered
	steps := []string{}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		done <- shutdownBindingGeneration(ctx, server, onceCleanup(func() { steps = append(steps, "cleanup") }), func(context.Context) error { steps = append(steps, "join"); return nil })
	}()
	select {
	case <-done:
		t.Fatal("restart did not wait for old inflight request")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-finished
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0] != "cleanup" || steps[1] != "join" {
		t.Fatalf("order %v", steps)
	}
}
func TestBindingRestartWorkerFailureNeverStartsNewGeneration(t *testing.T) {
	server := &http.Server{}
	calls := 0
	err := shutdownBindingGeneration(context.Background(), server, onceCleanup(func() { calls++ }), func(context.Context) error { return errors.New("workers remain") })
	if err == nil || calls != 1 {
		t.Fatalf("restart gate error=%v cleanup=%d", err, calls)
	}
	resetBindingGenerationHealth()
	markBindingGenerationUnhealthy()
	if bindingGenerationReadyToRestart() {
		t.Fatal("unhealthy cleanup permitted restart")
	}
	resetBindingGenerationHealth()
}
