package registry

import (
	"context"
	"encoding/json"
	"fmt"
)

// FetchVersions GETs <ecosystem>/<group>/versions.json and returns the decoded
// list of operator-release versions, sorted as served by the CDN (newest first).
// Maps HTTP 404 to ErrNotFound. Other non-2xx statuses are wrapped errors.
// Context cancellation is propagated.
func (c *Client) FetchVersions(ctx context.Context, ecosystem, group string) ([]string, error) {
	url := c.versionsURL(ecosystem, group)
	body, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	var v []string
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("registry: decoding versions from %s: %w", url, err)
	}
	return v, nil
}

// versionsURL returns "<baseURL>/<ecosystem>/<group>/versions.json".
func (c *Client) versionsURL(ecosystem, group string) string {
	return fmt.Sprintf("%s/%s/%s/versions.json", c.baseURL, ecosystem, group)
}
