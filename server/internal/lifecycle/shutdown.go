package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ShutdownConfig defines one process-wide budget. Drain must be shorter than
// Budget so cancellation, forced HTTP close, and worker joins have time to run.
type ShutdownConfig struct {
	Budget time.Duration
	Drain  time.Duration
}

// RunShutdown drains HTTP briefly, cancels all work, and waits for every tracked
// goroutine before returning to callers that close SQLite. If any phase hangs,
// the watchdog exits the process instead; this deliberately skips deferred DB
// close and avoids racing SQLite against live goroutines.
func RunShutdown(config ShutdownConfig, logf func(string, ...any), exit func(int),
	drain func(context.Context) error, cancel func(), wait func() error) error {
	if config.Budget <= 0 {
		return errors.New("shutdown budget must be positive")
	}
	if config.Drain <= 0 || config.Drain >= config.Budget {
		return errors.New("shutdown drain must be positive and shorter than the total budget")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if exit == nil {
		return errors.New("shutdown watchdog exit function is required")
	}
	started := time.Now()
	watchdog := time.AfterFunc(config.Budget, func() {
		logf("CRITICAL: total shutdown budget %s exceeded; exiting without closing SQLite while live goroutines may remain", config.Budget)
		exit(1)
	})

	drainCtx, drainCancel := context.WithTimeout(context.Background(), config.Drain)
	drainErr := drain(drainCtx)
	drainCancel()
	if drainErr != nil {
		logf("short shutdown drain ended after %s: %v", time.Since(started).Round(time.Millisecond), drainErr)
	}
	cancel()
	waitErr := wait()
	if !watchdog.Stop() {
		return errors.New("shutdown watchdog fired but process exit returned")
	}
	if elapsed := time.Since(started); elapsed > config.Budget {
		return fmt.Errorf("shutdown completed after budget: %s", elapsed)
	}
	return errors.Join(drainErr, waitErr)
}
