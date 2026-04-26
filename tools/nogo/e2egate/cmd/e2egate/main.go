// Command e2egate is a stdin-based gate over `go test -json` event streams.
// It exits 0 when at least one test executed and no package was wholly
// skipped or build-failed; otherwise 1 with reasons on stderr.
//
// Usage:
//
//	go test -tags=e2e -json ./tests/e2e/... | tee /tmp/e2e.json | e2egate
//
// The companion `tee` is the recommended pipeline form so the raw event
// stream is preserved for log archival; e2egate itself produces no stdout
// output, only an exit code and stderr reasons.
package main

import (
	"fmt"
	"os"

	"github.com/ghbvf/gocell/tools/nogo/e2egate"
)

func main() {
	res, err := e2egate.Parse(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2egate: %v\n", err)
		os.Exit(1)
	}
	if res.Failed() {
		fmt.Fprintln(os.Stderr, "e2egate: gate failed")
		for _, reason := range res.Reasons {
			fmt.Fprintf(os.Stderr, "  reason: %s\n", reason)
		}
		os.Exit(1)
	}
}
