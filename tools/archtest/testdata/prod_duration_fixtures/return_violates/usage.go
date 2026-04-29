// Package return_violates verifies that returning a literal duration from a
// function body is caught: 1 violation expected.
package return_violates

import "time"

func defaultTimeout() time.Duration {
	return 5 * time.Second
}
