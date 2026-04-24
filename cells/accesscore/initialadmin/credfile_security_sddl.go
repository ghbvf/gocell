package initialadmin

import (
	"fmt"
	"strings"
)

// sddlAce is one ACE parsed out of a DACL SDDL string.
//
// SDDL ACE format per MS-DTYP §2.5.1.1:
//
//	(<type>;<flags>;<rights>;<obj>;<inherit>;<sid>)
//
// where <sid> may be a full S-1-5-* form OR a well-known SDDL alias
// (SY for LocalSystem, BA for Built-in Administrators, LA for the local
// admin account, WD for Everyone, ...). Alias resolution depends on the
// running machine's SAM database; static enumeration is not feasible.
type sddlAce struct {
	aceType string
	sidStr  string
}

// parseDACLAces parses a DACL SDDL string (e.g. "D:PAI(A;;FA;;;LA)(D;;FA;;;WD)")
// and returns each ACE's type and SID string. The order of returned ACEs matches
// the order in the SDDL.
//
// Returns an error when the SDDL contains no ACEs or any ACE has fewer than
// six semicolon-delimited fields.
func parseDACLAces(sddl string) ([]sddlAce, error) {
	open := strings.Index(sddl, "(")
	if open < 0 {
		return nil, fmt.Errorf("no ACEs in SDDL: %q", sddl)
	}
	body := sddl[open:]
	var aces []sddlAce
	for {
		lp := strings.Index(body, "(")
		if lp < 0 {
			break
		}
		rp := strings.Index(body[lp:], ")")
		if rp < 0 {
			return nil, fmt.Errorf("unterminated ACE in SDDL: %q", body)
		}
		aceBody := body[lp+1 : lp+rp]
		parts := strings.Split(aceBody, ";")
		if len(parts) < 6 {
			return nil, fmt.Errorf("ACE has %d fields, want >=6: %q", len(parts), aceBody)
		}
		aces = append(aces, sddlAce{aceType: parts[0], sidStr: parts[5]})
		body = body[lp+rp+1:]
	}
	if len(aces) == 0 {
		return nil, fmt.Errorf("no ACEs parsed from SDDL: %q", sddl)
	}
	return aces, nil
}

// isAllowACEType reports whether the SDDL ACE type code grants access.
//
// ALLOW types per MS-DTYP §2.5.1.1:
//   - A   ACCESS_ALLOWED_ACE_TYPE
//   - AI  ACCESS_ALLOWED_ACE_TYPE with INHERITED
//   - AO  ACCESS_ALLOWED_OBJECT_ACE_TYPE
//
// DENY types (D, DI), audit types (AU, AL), and all object/callback variants
// not listed above are rejected — none of them grant access.
func isAllowACEType(t string) bool {
	switch t {
	case "A", "AI", "AO":
		return true
	default:
		return false
	}
}
