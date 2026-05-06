// Stub: hand-written fixture simulating gocell generate contract output.
// NOT generated — testdata only.

package get

import "net/http"

// Request — http.widget.get.v1.request
type Request struct {
	ID string `json:"id"`
}

// GetResponseObject is the sealed interface for http.widget.get.v1 responses.
type GetResponseObject interface {
	visitGetResponse(w http.ResponseWriter) error
}

// Get200JSONResponse renders HTTP 200.
type Get200JSONResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r Get200JSONResponse) visitGetResponse(w http.ResponseWriter) error { return nil }

// Get404ErrorResponse renders HTTP 404.
type Get404ErrorResponse struct {
	Message string `json:"message"`
}

func (r Get404ErrorResponse) visitGetResponse(w http.ResponseWriter) error { return nil }
