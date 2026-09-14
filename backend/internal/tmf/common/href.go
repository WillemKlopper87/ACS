package common

import (
	"fmt"
	"net/url"
	"strings"
)

// BuildHref constructs a deterministic absolute northbound href. collection is
// a trusted adapter-defined path (for example tmf-api/resourceInventory/v5/resource)
// while id is always escaped as one path segment.
func BuildHref(baseURL, collection, id string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	collection = strings.Trim(strings.TrimSpace(collection), "/")
	id = strings.TrimSpace(id)

	if baseURL == "" {
		return "", fmt.Errorf("base URL is required")
	}
	if collection == "" {
		return "", fmt.Errorf("collection path is required")
	}
	if id == "" {
		return "", fmt.Errorf("entity id is required")
	}
	if strings.Contains(collection, "..") {
		return "", fmt.Errorf("collection path must not contain parent traversal")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base URL must be absolute")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("base URL must not contain query or fragment")
	}

	basePath := strings.TrimRight(u.EscapedPath(), "/")
	escapedID := url.PathEscape(id)
	u.RawPath = ""
	u.Path = ""
	return strings.TrimRight(u.String(), "/") + basePath + "/" + collection + "/" + escapedID, nil
}
