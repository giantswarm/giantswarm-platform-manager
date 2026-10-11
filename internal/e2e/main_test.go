package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// heartbeatEvery keeps the package's output flowing below CI's no-output
// limit while a test runs long, so a hung test ends with go test's own
// timeout dump naming it rather than a silent kill of the step.
const heartbeatEvery = time.Minute

// TestMain runs the package only with E2E=1: its proofs run for minutes, past
// the per-package timeout of the unit tests, so `make test-e2e` (a
// prerequisite of `make test`) runs them on their own with a timeout of their
// own, and a plain `go test ./...` skips them.
func TestMain(m *testing.M) {
	if os.Getenv("E2E") != "1" {
		fmt.Println("skipped: set E2E=1 or run make test-e2e for the identity-chain proofs")
		os.Exit(0)
	}
	stop := heartbeat(heartbeatEvery)
	code := m.Run()
	stop()
	os.Exit(code)
}

// heartbeat prints the elapsed time to stderr at every tick until stopped.
func heartbeat(every time.Duration) (stop func()) {
	start := time.Now()
	ticker := time.NewTicker(every)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "e2e: running for %s\n", time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
	}
}
