// Package bcryptcostredfixture is a known-positive for BCRYPT-COST-FUNNEL-01
// rule A1: it deliberately calls bcrypt.GenerateFromPassword outside
// corecells/accesscore/internal/credential/hasher.go. The archtest scanner excludes
// the tools/archtest/internal tree from the production scan, so this fixture
// never appears in A1's real findings; TestBCRYPT_COST_FUNNEL_01_A1_RedFixture
// parses it directly to prove the detector fires.
package bcryptcostredfixture

import "golang.org/x/crypto/bcrypt"

// HashOutsideFunnel is the forbidden shape: a direct bcrypt.GenerateFromPassword
// call outside the sanctioned credential.Hasher.
func HashOutsideFunnel(password []byte) ([]byte, error) {
	return bcrypt.GenerateFromPassword(password, bcrypt.MinCost)
}
