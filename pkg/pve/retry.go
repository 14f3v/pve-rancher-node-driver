package pve

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

const maxBackoff = 30 * time.Second

// Retry runs fn up to attempts times, backing off exponentially (base, 2x,
// 4x... capped at 30s) with ±50% jitter, as long as retryable(err) is true.
func Retry(ctx context.Context, attempts int, base time.Duration, retryable func(error) bool, fn func() error) error {
	var err error
	delay := base
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		if i == attempts {
			break
		}
		jittered := delay/2 + time.Duration(rand.Int63n(int64(delay)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jittered):
		}
		if delay *= 2; delay > maxBackoff {
			delay = maxBackoff
		}
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}
