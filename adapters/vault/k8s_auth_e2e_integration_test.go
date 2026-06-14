//go:build integration

package vault_test

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"github.com/testcontainers/testcontainers-go/network"
	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/tests/testutil"
)

// ---------------------------------------------------------------------------
// TC-INT-K8S: real Kubernetes-auth e2e (issue #636, PR-A8-RESIDUAL).
//
// Unlike auth_test.go (httptest mock) this exercises the *real* Vault
// kubernetes auth method against a live API server: a k3s container provides
// TokenReview, a ServiceAccount JWT is minted via kubectl, and the provider
// authenticates with VAULT_AUTH_METHOD=kubernetes before a transit round-trip.
//
//	chain: SA JWT (file) -> auth/kubernetes/login -> TokenReview(k3s)
//	       -> Vault token (policy gocell-transit) -> transit datakey/decrypt
//
// Network model: Vault and k3s share a docker network. The Go test reaches
// Vault over the host-mapped loopback address (TLS-exempt); Vault reaches the
// k3s API server at https://<k3sNetworkAlias>:6443 — k3s is started with an
// extra --tls-san for that alias so its serving cert validates against the
// cluster CA we hand Vault. Only a missing Docker daemon may skip
// (testutil.RequireDocker); every other failure is fatal (no t.Skip).
// ---------------------------------------------------------------------------

const (
	k3sNetworkAlias   = "k3s-server"
	k8sNamespace      = "default"
	k8sAppSA          = "gocell"         // app identity Vault binds the role to
	k8sReviewerSA     = "vault-reviewer" // TokenReview caller (system:auth-delegator)
	vaultK8sRole      = "gocell"
	vaultTransitKey   = "gocell-config"
	vaultK8sMountPath = "kubernetes"
	// k8sTokenAudience binds the app SA token to a Vault-specific audience: the
	// token is minted with --audience and the Vault role pins
	// bound_service_account_audiences to the same value, mirroring the
	// least-privilege production wiring for K8s 1.21+ audience-scoped tokens.
	k8sTokenAudience = "vault"
)

// k3sAPIServerReadyTimeout bounds the readiness poll for the k3s apiserver to
// begin accepting requests after k3s.Run returns (which fires on a log line that
// can briefly precede write-readiness); site-specific to container cold start,
// so a file-local const rather than a pkg/testutil/testtime cross-cutting value.
const k3sAPIServerReadyTimeout = 60 * time.Second

// gocellTransitPolicyHCL grants the capabilities the envelope provider needs:
// encrypt routes through transit/datakey/plaintext (server-side DEK
// generation), decrypt through transit/decrypt, plus key read/rotate. Mirrors
// the policy used by TestTransitEnvelope_AppRoleAuth_RoundTrip.
const gocellTransitPolicyHCL = `
path "transit/datakey/plaintext/gocell-config" { capabilities = ["create","update"] }
path "transit/decrypt/gocell-config"           { capabilities = ["create","update"] }
path "transit/keys/gocell-config"              { capabilities = ["read"] }
path "transit/keys/gocell-config/rotate"       { capabilities = ["create","update"] }
`

// TestK8sAuth_RealE2E drives the production Kubernetes-auth path end to end and
// asserts the k8s-issued token is renewable (real-mode F-4a) and usable for a
// transit encrypt/decrypt round-trip.
func TestK8sAuth_RealE2E(t *testing.T) {
	testutil.RequireDocker(t)
	ctx := context.Background()

	// t.Cleanup runs LIFO: the containers below are registered after the network
	// and are therefore torn down before the network they attach to is removed.
	nw, err := network.New(ctx)
	require.NoError(t, err, "create shared docker network")
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	// --- k3s: live Kubernetes API server for TokenReview ---------------------
	k3sC, err := k3s.Run(ctx, testutil.K3sImage,
		network.WithNetwork([]string{k3sNetworkAlias}, nw),
		// Add the network alias to the apiserver serving-cert SANs. Without it,
		// Vault's TokenReview client dialing https://k3s-server:6443 would get an
		// x509 "certificate is valid for <default-SANs>, not k3s-server" error,
		// since k3s's self-signed serving cert only covers its startup SAN list.
		// Using --tls-san keeps real TLS validation (no tls_skip_verify bypass).
		testcontainers.WithCmdArgs("--tls-san="+k3sNetworkAlias),
	)
	require.NoError(t, err, "start k3s container")
	t.Cleanup(func() { _ = k3sC.Terminate(ctx) })

	appJWT, reviewerJWT, caPEM := provisionK8sAuthMaterial(ctx, t, k3sC)

	// --- Vault: same network so it can dial the k3s apiserver ----------------
	vaultAddr, rootToken, vaultTeardown := startVaultContainer(t,
		network.WithNetwork([]string{"vault"}, nw))
	t.Cleanup(vaultTeardown)

	configureVaultK8sAuth(ctx, t, vaultAddr, rootToken, reviewerJWT, caPEM)

	// --- App side: SA JWT on disk + env for the production constructor --------
	jwtPath := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(jwtPath, []byte(appJWT), 0o600), "write SA JWT")

	t.Setenv("VAULT_ADDR", vaultAddr)
	t.Setenv("VAULT_AUTH_METHOD", "kubernetes")
	t.Setenv("VAULT_K8S_ROLE", vaultK8sRole)
	t.Setenv("VAULT_K8S_JWT_PATH", jwtPath)
	t.Setenv("VAULT_K8S_MOUNT", vaultK8sMountPath)
	// Mount paths match the production defaults — set explicitly to verify the
	// wiring path, not to exercise non-standard mounts.
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", vaultTransitKey)

	// realMode=true exercises AssertForRealMode (kubernetes is accepted) and the
	// F-4a renewable-token guard. Construction performs the real K8s login.
	p, err := vaultadapter.NewTransitKeyProviderFromEnv(true /* realMode */, clock.Real(), mustTransitMetrics(t))
	require.NoError(t, err, "kubernetes-auth provider construction (real login)")
	t.Cleanup(func() { _ = p.Close(context.Background()) })

	require.True(t, p.Renewable(), "k8s-issued token must be renewable in real mode")

	// Transit round-trip proves the k8s-issued token carries the transit policy.
	handle, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle.ID(), "vault-transit:v")

	plaintext := []byte("k8s-auth-secret")
	aad := []byte("cell:configcore/key:k8s_test")

	result, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Ciphertext, "ciphertext must be non-empty")
	assert.NotEmpty(t, result.EDK, "edk (wrapped DEK) must be present for envelope mode")

	recovered, err := handle.Decrypt(ctx, result.Ciphertext, result.Nonce, result.EDK, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered, "kubernetes auth: round-trip must recover plaintext")
}

// provisionK8sAuthMaterial creates the reviewer SA (with system:auth-delegator
// for TokenReview), the app SA, mints a JWT for each, and returns the app JWT,
// the reviewer JWT, and the cluster CA (PEM) parsed from the kubeconfig.
func provisionK8sAuthMaterial(ctx context.Context, t *testing.T, k3sC *k3s.K3sContainer) (appJWT, reviewerJWT, caPEM string) {
	t.Helper()

	// tryKubectl runs kubectl and returns (output, exitCode, execErr) without
	// failing the test, for the readiness poll below.
	tryKubectl := func(args ...string) (string, int, error) {
		// The k3s image symlinks `kubectl` to the k3s binary, which auto-uses the
		// in-container kubeconfig at /etc/rancher/k3s/k3s.yaml; Multiplexed()
		// de-frames stdout/stderr.
		code, reader, execErr := k3sC.Exec(ctx, append([]string{"kubectl"}, args...), tcexec.Multiplexed())
		return readAllString(t, reader), code, execErr
	}
	kubectl := func(args ...string) string {
		out, code, execErr := tryKubectl(args...)
		require.NoErrorf(t, execErr, "k3s kubectl %v: %s", args, out)
		require.Zerof(t, code, "k3s kubectl %v exit=%d: %s", args, code, out)
		return out
	}

	// k3s.Run returns on the "Node controller sync successful" log, which can
	// briefly precede the apiserver accepting write requests; poll a read before
	// the first create so a startup-window blip surfaces as a clear timeout, not
	// a spurious create failure.
	testwait.External(t, "k3s-apiserver-ready", func() bool {
		_, code, execErr := tryKubectl("get", "--raw=/readyz")
		return execErr == nil && code == 0
	}, k3sAPIServerReadyTimeout, time.Second, "k3s apiserver must become ready for requests")

	kubectl("create", "serviceaccount", k8sReviewerSA, "-n", k8sNamespace)
	kubectl("create", "clusterrolebinding", "vault-auth-delegator",
		"--clusterrole=system:auth-delegator",
		"--serviceaccount="+k8sNamespace+":"+k8sReviewerSA)
	// The reviewer JWT only authenticates Vault's TokenReview call (audience =
	// apiserver default, so no --audience); a short TTL suffices — the container
	// outlives neither.
	reviewerJWT = requireJWT(t, kubectl("create", "token", k8sReviewerSA, "-n", k8sNamespace, "--duration=1h"), "reviewer")

	kubectl("create", "serviceaccount", k8sAppSA, "-n", k8sNamespace)
	// The app JWT is audience-bound to k8sTokenAudience; the Vault role pins the
	// same audience, so TokenReview validates the aud claim (K8s 1.21+ model).
	appJWT = requireJWT(t, kubectl("create", "token", k8sAppSA, "-n", k8sNamespace,
		"--audience="+k8sTokenAudience, "--duration=1h"), "app")

	kubeconfig, err := k3sC.GetKubeConfig(ctx)
	require.NoError(t, err, "read kubeconfig")
	caPEM = extractClusterCA(t, kubeconfig)
	return appJWT, reviewerJWT, caPEM
}

// requireJWT trims kubectl output and asserts it looks like a JWT (base64url
// "eyJ" header), so unexpected kubectl preamble surfaces here rather than as a
// cryptic Vault login error downstream.
//
// On failure it reports only the byte length, never the value: `kubectl create
// token` succeeds (exit 0) yet can emit a warning/preamble inline with the
// minted SA JWT (e.g. "Warning: ...\n<token>"), which TrimSpace leaves in `jwt`.
// Printing %q would then leak a live (short-lived) credential into the CI log.
// The length alone distinguishes a short error preamble from a ~kilobyte token.
func requireJWT(t *testing.T, raw, label string) string {
	t.Helper()
	jwt := strings.TrimSpace(raw)
	require.Truef(t, strings.HasPrefix(jwt, "eyJ"),
		"%s token must be a JWT (base64url 'eyJ' header); got %d bytes not starting with eyJ (value withheld to avoid logging a credential)",
		label, len(jwt))
	return jwt
}

// configureVaultK8sAuth enables the kubernetes auth method, points it at the
// k3s apiserver (reachable over the shared network), installs the transit
// policy, and binds the app ServiceAccount to it.
func configureVaultK8sAuth(ctx context.Context, t *testing.T, addr, rootToken, reviewerJWT, caPEM string) {
	t.Helper()

	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	admin, err := vaultapi.NewClient(cfg)
	require.NoError(t, err, "create admin vault client")
	// Admin-only client: SetToken pins the root token explicitly, overriding any
	// VAULT_TOKEN that DefaultConfig may have read from the environment.
	admin.SetToken(rootToken)

	write := func(path string, data map[string]any) {
		_, werr := admin.Logical().WriteWithContext(ctx, path, data)
		require.NoErrorf(t, werr, "vault write %s", path)
	}

	write("sys/auth/"+vaultK8sMountPath, map[string]any{"type": "kubernetes"})
	write("auth/"+vaultK8sMountPath+"/config", map[string]any{
		"kubernetes_host":    "https://" + k3sNetworkAlias + ":6443",
		"kubernetes_ca_cert": caPEM,
		"token_reviewer_jwt": reviewerJWT,
	})
	write("sys/policies/acl/gocell-transit", map[string]any{"policy": gocellTransitPolicyHCL})
	write("auth/"+vaultK8sMountPath+"/role/"+vaultK8sRole, map[string]any{
		"bound_service_account_names":      k8sAppSA,
		"bound_service_account_namespaces": k8sNamespace,
		"bound_service_account_audiences":  k8sTokenAudience,
		"token_policies":                   "gocell-transit",
		"token_ttl":                        "1h",
		"token_max_ttl":                    "2h",
	})
}

// extractClusterCA parses a kubeconfig and returns the base64-decoded
// certificate-authority-data of the first cluster as a PEM string.
func extractClusterCA(t *testing.T, kubeconfig []byte) string {
	t.Helper()
	var kc struct {
		Clusters []struct {
			Cluster struct {
				CAData string `yaml:"certificate-authority-data"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	require.NoError(t, yaml.Unmarshal(kubeconfig, &kc), "parse kubeconfig")
	require.NotEmpty(t, kc.Clusters, "kubeconfig must declare a cluster")
	pem, err := base64.StdEncoding.DecodeString(kc.Clusters[0].Cluster.CAData)
	require.NoError(t, err, "decode certificate-authority-data")
	require.NotEmpty(t, pem, "cluster CA must be non-empty")
	return string(pem)
}

func readAllString(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	require.NoError(t, err, "read exec output")
	return string(b)
}
