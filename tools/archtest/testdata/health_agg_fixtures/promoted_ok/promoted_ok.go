// Package promotedok embeds *base.PGResource so that Checkers/Worker/Close
// reach the type via promoted methods. HEALTH-AGG-01 must NOT flag App.
package promotedok

import "healthaggfixtures/base"

type App struct {
	*base.PGResource
}
