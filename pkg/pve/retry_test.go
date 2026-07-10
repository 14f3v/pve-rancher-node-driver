package pve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrySucceedsAfterRetryable(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), 5, time.Millisecond, IsLockErr, func() error {
		calls++
		if calls < 3 {
			return errors.New("got lock request timeout")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestRetryStopsOnNonRetryable(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), 5, time.Millisecond, IsLockErr, func() error {
		calls++
		return errors.New("permanent problem")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryExhausts(t *testing.T) {
	err := Retry(context.Background(), 3, time.Millisecond, IsLockErr, func() error {
		return errors.New("got lock request timeout")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 3 attempts")
}

func TestRetryHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Retry(ctx, 5, 10*time.Second, IsLockErr, func() error {
		return errors.New("got lock request timeout")
	})
	assert.ErrorIs(t, err, context.Canceled)
}
