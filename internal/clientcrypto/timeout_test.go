package clientcrypto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
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
			// Every goroutine, like go test -timeout prints: only the timer's own
			// stack would not show where the test hung.
			debug.SetTraceback("all")
			panic(fmt.Sprintf("%s exceeded its %s budget", t.Name(), d))
		}
	})
}

// testFunc matches a test function and captures its parameter name and its
// first statement line.
var testFunc = regexp.MustCompile(`(?m)^func (Test\w+)\((\w+) \*testing\.T\) \{\n([^\n]*)`)

// checkEveryTestHasABudget keeps the documented rule true
// (docs/architecture/ci-testing.md, "Go Tests"): the first statement of every
// test function in this directory is its withTimeout call. The call itself is
// required, whatever the parameter is named, so a comment naming it does not
// count.
func checkEveryTestHasABudget(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range testFunc.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasPrefix(match[3], "\twithTimeout("+match[2]+", ") {
				t.Errorf("%s: %s does not start with a withTimeout budget", file, match[1])
			}
		}
	}
}

func TestEveryTestRunsUnderABudget(t *testing.T) {
	withTimeout(t, 30*time.Second)
	checkEveryTestHasABudget(t)
}
