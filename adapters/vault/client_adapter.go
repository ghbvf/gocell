package vault

import (
	"context"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// vaultAPIClient wraps *vaultapi.Client to satisfy the vaultClient interface.
// This is the production wiring between vault/api and TransitKeyProvider.
//
// Migrated from runtime/crypto.vaultAPIClient (R1c Phase 0-c). The adapter
// itself carries no envelope semantics — it is a thin I/O shim.
//
// ref: hashicorp/vault api/client.go — vaultapi.Client.Logical().WriteWithContext
type vaultAPIClient struct {
	client *vaultapi.Client
}

// NewVaultAPIClient wraps the provided *vaultapi.Client in the VaultClient
// adapter used by TransitKeyProvider. Use this when constructing the provider
// from a pre-configured *vaultapi.Client (e.g. in tests or when the caller
// manages Vault authentication separately from NewTransitKeyProviderFromEnv).
func NewVaultAPIClient(c *vaultapi.Client) VaultClient {
	return &vaultAPIClient{client: c}
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
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound,
			"vault api: read returned nil response for path: "+path)
	}
	return resp.Data, nil
}

// LookupSelfToken implements TokenRenewer. It calls auth/token/lookup-self to
// retrieve the current token's metadata (TTL, renewable flag, accessor) and
// returns a *vaultapi.Secret suitable for seeding a LifetimeWatcher.
//
// ref: hashicorp/vault api/auth_token.go@main — LookupSelfWithContext
func (a *vaultAPIClient) LookupSelfToken(ctx context.Context) (*vaultapi.Secret, error) {
	secret, err := a.client.Auth().Token().LookupSelfWithContext(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault api: lookup self token", err)
	}
	return secret, nil
}

// NewLifetimeWatcher implements TokenRenewer. It wraps client.NewLifetimeWatcher
// to create a watcher that automatically renews the token at ~2/3 of its TTL.
//
// ref: hashicorp/vault api/lifetime_watcher.go@main — Client.NewLifetimeWatcher
func (a *vaultAPIClient) NewLifetimeWatcher(i *vaultapi.LifetimeWatcherInput) (*vaultapi.LifetimeWatcher, error) {
	w, err := a.client.NewLifetimeWatcher(i)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault api: create lifetime watcher", err)
	}
	return w, nil
}
