package flickcli

import (
	"fmt"
	"testing"
	"time"
)

// withTimeout gives one test a wall-clock budget the way `go test -timeout`
// gives the binary one: over budget the process unwedges immediately, so a
// single hung flow cannot hold the suite or CI. Durations stay generous
// (10x the slowest observed test).
func withTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	time.AfterFunc(d, func() {
		select {
		case <-done:
		default:
			panic(fmt.Sprintf("%s exceeded its %s budget", t.Name(), d))
		}
	})
}
