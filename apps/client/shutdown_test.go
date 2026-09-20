package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClientHTTPShutdown struct {
	mu              sync.Mutex
	onShutdown      func()
	listenersClosed bool
	waitForContext  bool
	skipCallback    bool
}

func (s *fakeClientHTTPShutdown) RegisterOnShutdown(callback func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onShutdown = callback
}

func (s *fakeClientHTTPShutdown) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.listenersClosed = true
	callback := s.onShutdown
	s.mu.Unlock()
	if callback != nil && !s.skipCallback {
		callback()
	}
	if s.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (s *fakeClientHTTPShutdown) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listenersClosed
}

type fakeClientTransferShutdown struct {
	server         *fakeClientHTTPShutdown
	waitForContext bool
	mu             sync.Mutex
	called         bool
	calledTooEarly bool
}

func (s *fakeClientTransferShutdown) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.called = true
	s.calledTooEarly = !s.server.stopped()
	s.mu.Unlock()
	if s.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestShutdownClientStopsHTTPBeforeTransfers(t *testing.T) {
	server := &fakeClientHTTPShutdown{}
	transfers := &fakeClientTransferShutdown{server: server}
	if err := shutdownClient(context.Background(), server, transfers); err != nil {
		t.Fatal(err)
	}
	transfers.mu.Lock()
	defer transfers.mu.Unlock()
	if !transfers.called {
		t.Fatal("transfer shutdown was not called")
	}
	if transfers.calledTooEarly {
		t.Fatal("transfer shutdown started before HTTP listeners were closed")
	}
}

func TestShutdownClientHonorsContextDeadline(t *testing.T) {
	server := &fakeClientHTTPShutdown{waitForContext: true, skipCallback: true}
	transfers := &fakeClientTransferShutdown{server: server, waitForContext: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := shutdownClient(ctx, server, transfers)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown exceeded its deadline by too much: %v", elapsed)
	}
	transfers.mu.Lock()
	defer transfers.mu.Unlock()
	if !transfers.called {
		t.Fatal("transfer shutdown was skipped when HTTP callback did not run")
	}
}
