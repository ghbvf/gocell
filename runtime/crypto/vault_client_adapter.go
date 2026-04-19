package crypto

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	vaultapi "github.com/hashicorp/vault/api"
)

// vaultAPIClient wraps *vaultapi.Client to satisfy the vaultClient interface.
// This adapter is the production wiring between vault/api and VaultTransitKeyProvider.
//
// ref: hashicorp/vault api/client.go — vaultapi.Client.Logical().WriteWithContext
type vaultAPIClient struct {
	client *vaultapi.Client
}

// Write sends a PUT/POST to the given Vault path with the provided data.
func (a *vaultAPIClient) Write(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	resp, err := a.client.Logical().WriteWithContext(ctx, path, data)
	if err != nil {
		return nil, fmt.Errorf("vault api: write %s: %w", path, err)
	}
	if resp == nil {
		// Some write operations (e.g. rotate) return no data — treat as empty map.
		return map[string]any{}, nil
	}
	return resp.Data, nil
}

// Read sends a GET to the given Vault path and returns the data map.
func (a *vaultAPIClient) Read(ctx context.Context, path string) (map[string]any, error) {
	resp, err := a.client.Logical().ReadWithContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("vault api: read %s: %w", path, err)
	}
	if resp == nil {
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			"vault api: read returned nil response for path: "+path)
	}
	return resp.Data, nil
}
