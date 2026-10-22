package agent

import (
	"context"
	"errors"
)

// Classified is implemented by adapter errors that know why they failed. The
// API adapter uses this to distinguish rate limits and auth failures from
// permanent errors without the worker importing the adapters package (which
// would be an import cycle).
type Classified interface {
	FailureKind() string
}

// FailureKinds used by the worker's retry policy.
const (
	FailureTransient  = "transient"
	FailureRateLimit  = "rate_limited"
	FailureAuth       = "auth"
	FailureCapability = "capability"
	FailurePermanent  = "permanent"
)

// FailureKind classifies an adapter error. Transient failures are retried with
// backoff while attempts remain; everything else fails the job.
func FailureKind(err error) string {
	if err == nil {
		return ""
	}
	var c Classified
	if errors.As(err, &c) {
		if k := c.FailureKind(); k != "" {
			return k
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return FailureTransient
	case errors.Is(err, context.Canceled):
		return FailureTransient
	default:
		return FailurePermanent
	}
}
