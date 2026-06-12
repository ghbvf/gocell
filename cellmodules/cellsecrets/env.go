package cellsecrets

import "os"

// LoadCursorKeys reads the cursor HMAC primary and previous keys for a cell.
//
// Env variables read:
//
//	GOCELL_<CELLID>_CURSOR_KEY
//	GOCELL_<CELLID>_CURSOR_PREVIOUS_KEY
func LoadCursorKeys(cellEnvPrefix string) (primary, previous string) {
	prefix := "GOCELL_" + cellEnvPrefix + "_CURSOR_"
	return os.Getenv(prefix + "KEY"), os.Getenv(prefix + "PREVIOUS_KEY")
}

// LoadCellHMACKey reads the HMAC key env var for a specific cell using the
// per-cell namespace convention (GOCELL_<CELLID>_HMAC_KEY).
func LoadCellHMACKey(cellEnvPrefix string) string {
	return os.Getenv("GOCELL_" + cellEnvPrefix + "_HMAC_KEY")
}

// LoadConfigCoreKeyProvider reads the configcore-specific KeyProvider
// configuration:
//
//	GOCELL_CONFIGCORE_KEY_PROVIDER
//	GOCELL_CONFIGCORE_MASTER_KEY
//	GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS
func LoadConfigCoreKeyProvider() (providerName, masterKey, prevMasterKey string) {
	return os.Getenv("GOCELL_CONFIGCORE_KEY_PROVIDER"),
		os.Getenv("GOCELL_CONFIGCORE_MASTER_KEY"),
		os.Getenv("GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS")
}

// LoadWebhookSourceKeyProvider reads the KeyProvider configuration for the
// persistent, encrypted webhook source secret store (#1540). See
// cellmodules/webhooksource.buildKeyProviderFromName for provider semantics:
//
//	GOCELL_WEBHOOK_KEY_PROVIDER        (required in postgres mode: "local-aes" | "vault-transit")
//	GOCELL_WEBHOOK_MASTER_KEY          (required for local-aes; ignored for vault-transit)
//	GOCELL_WEBHOOK_MASTER_KEY_PREVIOUS (optional rotation key; local-aes only)
func LoadWebhookSourceKeyProvider() (providerName, masterKey, prevMasterKey string) {
	return os.Getenv("GOCELL_WEBHOOK_KEY_PROVIDER"),
		os.Getenv("GOCELL_WEBHOOK_MASTER_KEY"),
		os.Getenv("GOCELL_WEBHOOK_MASTER_KEY_PREVIOUS")
}
