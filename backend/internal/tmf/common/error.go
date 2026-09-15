// Package common contains version-neutral primitives shared by TM Forum
// northbound adapters. It deliberately does not depend on generated TMF DTOs
// or protocol-specific CWMP/USP packages.
package common

import "fmt"

// APIError is the internal representation of an error exposed through a TMF
// northbound API. HTTP/version-specific handlers map it to the exact error DTO
// required by the selected TMF contract.
type APIError struct {
	Status         int
	Code           string
	Reason         string
	Message        string
	ReferenceError string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Reason != "" {
		return e.Reason
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("TMF API error (HTTP %d)", e.Status)
}

// InvalidQuery returns a consistent 400-class validation error for malformed
// northbound query parameters.
func InvalidQuery(parameter, detail string) *APIError {
	return &APIError{
		Status:  400,
		Code:    "INVALID_QUERY",
		Reason:  "Invalid query parameter",
		Message: fmt.Sprintf("%s: %s", parameter, detail),
	}
}

// NotFound deliberately carries no tenant-sensitive detail. Handlers should
// use the same error for a missing entity and an entity outside the caller's
// tenant scope so identifiers cannot be used as an existence oracle.
func NotFound(entity string) *APIError {
	return &APIError{
		Status:  404,
		Code:    "NOT_FOUND",
		Reason:  "Entity not found",
		Message: fmt.Sprintf("%s not found", entity),
	}
}
