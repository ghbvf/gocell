// Package aeadutil provides pure AES-GCM helpers shared across runtime/crypto
// and adapters/vault. All functions are stateless, have zero external
// dependencies (stdlib only), and follow the same nonce-handling and error-
// sanitisation conventions as google/tink-go aead/subtle and
// kubernetes/kubernetes kmsv2/envelope.go.
//
// ref: google/tink-go aead/subtle/aes_gcm.go — AEAD function signature convention
// ref: kubernetes/kubernetes kmsv2/envelope.go — envelope encryption pattern
// ref: aws/aws-sdk-go s3crypto — split nonce storage convention
package aeadutil
