package common

import (
	"net/url"
	"strconv"
)

const (
	DefaultPageLimit = 100
	MaxPageLimit     = 1000
)

// PageRequest is the version-neutral offset/limit model used by TMF list
// endpoints. Total/result count response headers remain an HTTP concern.
type PageRequest struct {
	Offset int
	Limit  int
}

// ParsePage parses TMF-style offset/limit parameters with explicit bounds.
// Unsupported or malformed values are rejected rather than silently widened.
func ParsePage(values url.Values, defaultLimit, maxLimit int) (PageRequest, error) {
	if defaultLimit <= 0 {
		defaultLimit = DefaultPageLimit
	}
	if maxLimit <= 0 {
		maxLimit = MaxPageLimit
	}
	if defaultLimit > maxLimit {
		defaultLimit = maxLimit
	}

	page := PageRequest{Limit: defaultLimit}

	if raw := values.Get("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return PageRequest{}, InvalidQuery("offset", "must be a non-negative integer")
		}
		page.Offset = offset
	}

	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			return PageRequest{}, InvalidQuery("limit", "must be a positive integer")
		}
		if limit > maxLimit {
			return PageRequest{}, InvalidQuery("limit", "exceeds the maximum allowed page size")
		}
		page.Limit = limit
	}

	return page, nil
}
