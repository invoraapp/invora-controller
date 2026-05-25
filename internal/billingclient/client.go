package billingclient

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

const defaultTimeout = 30 * time.Second

// APIError is returned for any non-2xx response from the billing gateway.
type APIError struct {
	StatusCode int
	Body       []byte
	Err        string
}

func (e *APIError) Error() string {
	if e.Err != "" {
		return fmt.Sprintf("billing api: %d: %s", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("billing api: %d: %s", e.StatusCode, string(e.Body))
}

// IsNotFound reports whether the error is a 404/NOT_FOUND.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}
