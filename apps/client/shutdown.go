package main

import (
	"context"
	"errors"
)

type clientHTTPShutdowner interface {
	RegisterOnShutdown(func())
	Shutdown(context.Context) error
}

type clientTransferShutdowner interface {
	Shutdown(context.Context) error
}

// shutdownClient starts HTTP shutdown first. net/http invokes registered
// callbacks only after it has closed its listeners, so no new request can race
// a transfer shutdown once transferService.Shutdown begins.
func shutdownClient(ctx context.Context, server clientHTTPShutdowner, transferService clientTransferShutdowner) error {
	httpStopped := make(chan struct{})
	server.RegisterOnShutdown(func() { close(httpStopped) })
	httpDone := make(chan error, 1)
	go func() {
		httpDone <- server.Shutdown(ctx)
	}()

	select {
	case <-httpStopped:
	case <-ctx.Done():
	}
	transferErr := transferService.Shutdown(ctx)

	var httpErr error
	select {
	case httpErr = <-httpDone:
	case <-ctx.Done():
		httpErr = ctx.Err()
	}
	return errors.Join(httpErr, transferErr)
}
