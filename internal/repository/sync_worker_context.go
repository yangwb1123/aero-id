package repository

import "context"

type syncWorkerContextKey struct{}

// WithSyncJobWorker annotates an internal persistence context with its lease
// owner. It is intentionally not part of the transport API.
func WithSyncJobWorker(ctx context.Context, worker string) context.Context {
	return context.WithValue(ctx, syncWorkerContextKey{}, worker)
}
