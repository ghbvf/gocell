// Package http provides HTTP adapter implementations for accesscore's outbound
// cross-cell calls.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// configEntryDataResponse mirrors the {data: {...}} envelope returned by
// GET /internal/v1/config/{key} (contract: http.config.internal.get.v1).
type configEntryDataResponse struct {
	Data struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Sensitive bool   `json:"sensitive"`
		Version   int    `json:"version"`
	} `json:"data"`
}

// HTTPConfigGetter calls configcore's internal GET /internal/v1/config/{key}
// endpoint. It signs every outbound request with a service token derived from
// the provided HMACKeyRing.
//
// contract: http.config.internal.get.v1
// ref: go-micro config/source/remote — polling + on-change patterns.
type HTTPConfigGetter struct {
	baseURL string
	ring    *auth.HMACKeyRing
	client  *http.Client
}

// NewHTTPConfigGetter creates a new HTTPConfigGetter.
// baseURL is the base address of the internal listener (e.g. "http://localhost:9090").
// ring is used to generate the service token Authorization header.
func NewHTTPConfigGetter(baseURL string, ring *auth.HMACKeyRing) *HTTPConfigGetter {
	return &HTTPConfigGetter{
		baseURL: baseURL,
		ring:    ring,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// NewHTTPConfigGetterWithHTTPClient creates a new HTTPConfigGetter with a custom
// *http.Client (used in tests with httptest.Server).
func NewHTTPConfigGetterWithHTTPClient(baseURL string, ring *auth.HMACKeyRing, httpClient *http.Client) *HTTPConfigGetter {
	return &HTTPConfigGetter{
		baseURL: baseURL,
		ring:    ring,
		client:  httpClient,
	}
}

// GetEntry fetches the current config entry for key from the configcore
// internal endpoint. Returns errcode.ErrConfigNotFound when the key does not
// exist (HTTP 404).
func (c *HTTPConfigGetter) GetEntry(ctx context.Context, key string) (ports.ConfigEntry, error) {
	path := "/internal/v1/config/" + url.PathEscape(key)
	fullURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: build request: %w", err)
	}

	// Sign the request with a service token so the InternalListener middleware accepts it.
	token := auth.GenerateServiceToken(c.ring, http.MethodGet, path, "", time.Now())
	req.Header.Set("Authorization", "ServiceToken "+token)

	resp, err := c.client.Do(req)
	if err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to decode
	case http.StatusNotFound:
		return ports.ConfigEntry{}, errcode.New(errcode.ErrConfigNotFound,
			fmt.Sprintf("config key %q not found", key))
	default:
		return ports.ConfigEntry{}, fmt.Errorf("configclient: unexpected status %d for key %q", resp.StatusCode, key)
	}

	var env configEntryDataResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return ports.ConfigEntry{}, fmt.Errorf("configclient: decode response: %w", err)
	}

	return ports.ConfigEntry{
		Key:       env.Data.Key,
		Value:     env.Data.Value,
		Sensitive: env.Data.Sensitive,
		Version:   env.Data.Version,
	}, nil
}

// Ensure HTTPConfigGetter implements ports.ConfigGetter at compile time.
var _ ports.ConfigGetter = (*HTTPConfigGetter)(nil)
