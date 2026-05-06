// Stub: hand-written fixture simulating gocell generate contract output.
// NOT generated — testdata only.

package get

import "context"

// Service is the business interface that http.widget.get.v1 server must implement.
type Service interface {
	Get(ctx context.Context, req *Request) (GetResponseObject, error)
}
