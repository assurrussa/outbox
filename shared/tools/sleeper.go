package tools

import (
	"context"
	"time"
)

func RunSleeper(ctx context.Context, duration time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}
