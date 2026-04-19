//go:build integration

package crypto_test

// VaultTransitKeyProvider integration tests.
//
// TODO(S14a): These tests require the vault testcontainer module and
// github.com/hashicorp/vault/api SDK. Both are unavailable in the current
// dependency graph (vault SDK not yet approved; testcontainers/vault module
// not included).
//
// Backlog: S14a CONFIG-VALUE-KMS-AWS-PROVIDER-01 — add vault/api SDK and
// testcontainers/vault module to go.mod; implement these test cases:
//
//   - TestVaultTransitKeyProvider_EncryptDecrypt_RoundTrip
//     testcontainers vault dev mode, mount transit, create key, encrypt/decrypt.
//
//   - TestVaultTransitKeyProvider_KeyRotation
//     call transit/keys/{name}/rotate, verify new writes use new version,
//     old ciphertext still decryptable.
//
//   - TestVaultTransitKeyProvider_NetworkFailure_FailClosed
//     stop vault container, call Encrypt/Decrypt, verify ErrKeyProviderDecryptFailed
//     is returned (never a partial success or empty plaintext).
//
//   - TestVaultTransitKeyProvider_ByID_ResolvesHistoricalVersion
//     verify keyID from old ciphertext routes to correct vault key version.
//
// When implementing:
//   1. go get github.com/hashicorp/vault/api@v1.14+
//   2. go get github.com/testcontainers/testcontainers-go/modules/vault@v0.41+
//   3. Implement VaultTransitKeyProvider.Current/ByID/Rotate using vault client.
//   4. Remove this TODO block.
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
