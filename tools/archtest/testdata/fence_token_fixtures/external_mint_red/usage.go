// Package external_mint_red is a RED fixture: it directly calls
// credentialfence.Mint from a non-allowlisted path. FENCE-TOKEN-MINT-FUNNEL-01
// must detect exactly 1 violation in this file.
package external_mint_red

import (
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// badMint constructs a FenceToken outside the credentialinvalidate funnel.
// This is the pattern the archtest bans: any non-funnel call to Mint is a
// upstream-funnel bypass.
func badMint() credentialfence.FenceToken {
	return credentialfence.Mint()
}
