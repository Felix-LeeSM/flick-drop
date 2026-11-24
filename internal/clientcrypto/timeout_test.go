package clientcrypto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
			panic(fmt.Sprintf("%s exceeded its %s budget", t.Name(), d))
		}
	})
}

// checkEveryTestHasABudget keeps the documented rule true
// (docs/architecture/ci-testing.md, "Go Tests"): every test function in this
// directory starts with withTimeout, including ones added later.
func checkEveryTestHasABudget(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	testFunc := regexp.MustCompile(`(?m)^func (Test\w+)\(t \*testing\.T\) \{\n(\t[^\n]*)`)
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range testFunc.FindAllStringSubmatch(string(src), -1) {
			if !strings.Contains(match[2], "withTimeout(t,") {
				t.Errorf("%s: %s has no withTimeout budget", file, match[1])
			}
		}
	}
}

func TestEveryTestRunsUnderABudget(t *testing.T) {
	withTimeout(t, 30*time.Second)
	checkEveryTestHasABudget(t)
}
