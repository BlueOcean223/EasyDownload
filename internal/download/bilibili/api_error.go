package bilibili

import "fmt"

// bilibiliAPIError preserves both transport HTTP status and Bilibili's JSON
// business code so the adapter can distinguish authentication and risk-control
// failures from retryable service outages.
type bilibiliAPIError struct {
	StatusCode int
	Code       int
	Message    string
}

func (err *bilibiliAPIError) Error() string {
	if err == nil {
		return ""
	}
	if err.StatusCode != 0 {
		return fmt.Sprintf("Bilibili API HTTP status %d", err.StatusCode)
	}
	return fmt.Sprintf("Bilibili API code %d: %s", err.Code, err.Message)
}
