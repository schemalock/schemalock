package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultUserAgent = "schemalock/0.1 (+https://schemalock.dev)"
	defaultTimeout   = 30 * time.Second
	// MaxBodyBytes is the maximum number of bytes the client reads from any single
	// HTTP response. Exported so tests can construct just-over-limit payloads.
	MaxBodyBytes = 16 << 20 // 16 MiB
)

// Client fetches manifests and schemas from a SchemaLock-compatible CDN
// (currently only cdn.schemalock.dev for the PoC).
type Client struct {
	baseURL   string
	http      *http.Client
	userAgent string
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient replaces the default HTTP client with the provided one.
// Use this to inject a custom transport, timeout, or test double.
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *Client) {
		c.http = h
	}
}

// WithUserAgent overrides the User-Agent header sent on every CDN request.
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// NewClient constructs a Client targeting baseURL. It applies opts in order.
// Defaults: 30-second timeout HTTP client, User-Agent "schemalock/0.1 (+https://schemalock.dev)".
func NewClient(baseURL string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL:   baseURL,
		http:      &http.Client{Timeout: defaultTimeout},
		userAgent: defaultUserAgent,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Manifest is the on-the-wire shape served at <ecosystem>/<group>/<version>/manifest.json.
// It mirrors ingest/internal/manifest.Manifest exactly so a live manifest.json
// from cdn.schemalock.dev decodes without modification.
type Manifest struct {
	// Version is the shape version of the manifest file itself (currently always 1).
	Version int `json:"version"`
	// Kinds maps each Kind name (capitalized) to its integrity hash and byte size.
	Kinds map[string]KindEntry `json:"kinds"`
}

// KindEntry holds the integrity hash and byte size for a single schema file.
type KindEntry struct {
	// Integrity is the W3C SRI hash in "sha256-<base64-std>" format.
	Integrity string `json:"integrity"`
	// Size is the byte count of the uploaded <Kind>.json file.
	Size int `json:"size"`
	// Deprecated is true when the CRD is marked deprecated by the upstream operator.
	Deprecated bool `json:"deprecated,omitempty"`
	// Description is the root schema description (trimmed).
	Description string `json:"description,omitempty"`
}

// FetchManifest GETs <ecosystem>/<group>/<version>/manifest.json and
// returns the decoded Manifest. Context cancellation is propagated.
func (c *Client) FetchManifest(ctx context.Context, ecosystem, group, version string) (Manifest, error) {
	url := c.manifestURL(ecosystem, group, version)
	body, err := c.get(ctx, url)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return Manifest{}, fmt.Errorf("registry: decoding manifest from %s: %w", url, err)
	}
	return m, nil
}

// FetchSchema GETs <ecosystem>/<group>/<version>/<kind>.json and returns the
// raw response bytes verbatim. No integrity verification is performed here —
// the caller (e.g. cache.WriteSchema) verifies before persisting.
// Context cancellation is propagated.
func (c *Client) FetchSchema(ctx context.Context, ecosystem, group, version, kind string) ([]byte, error) {
	url := c.schemaURL(ecosystem, group, version, kind)
	return c.get(ctx, url)
}

// get performs a GET request to url, sets the User-Agent header, and returns
// the response body. It maps HTTP 404 to ErrNotFound and wraps all other
// errors with the URL in the message.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("registry: building request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("registry: GET %s: %w", url, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registry: GET %s: unexpected status %d", url, resp.StatusCode)
	}

	lr := io.LimitReader(resp.Body, MaxBodyBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("registry: GET %s: reading body: %w", url, err)
	}
	if int64(len(data)) > MaxBodyBytes {
		return nil, fmt.Errorf("registry: GET %s: response too large (> %d bytes)", url, MaxBodyBytes)
	}
	return data, nil
}

// manifestURL builds the CDN URL for a manifest.json file.
func (c *Client) manifestURL(ecosystem, group, version string) string {
	base := strings.TrimRight(c.baseURL, "/")
	return base + "/" + ecosystem + "/" + group + "/" + version + "/manifest.json"
}

// schemaURL builds the CDN URL for a <kind>.json schema file.
func (c *Client) schemaURL(ecosystem, group, version, kind string) string {
	base := strings.TrimRight(c.baseURL, "/")
	return base + "/" + ecosystem + "/" + group + "/" + version + "/" + kind + ".json"
}
