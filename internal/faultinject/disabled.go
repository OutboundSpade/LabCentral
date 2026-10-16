//go:build !faultinject

// Package faultinject provides qualification-only mutation boundaries. Normal
// binaries never inspect fault injection environment variables.
package faultinject

import "context"

func WithOperation(ctx context.Context, platform, operation string) context.Context { return ctx }
func Call[T any](ctx context.Context, step string, call func(context.Context) (T, error)) (T, error) {
	return call(ctx)
}
func Rejected(error) bool { return false }

// Sent marks an accepted SSH exec request in qualification builds.
func Sent(context.Context) {}

func Status(error) int                   { return 0 }
func Trace(context.Context, string, int) {}

func TraceError(context.Context, string, error) {}
