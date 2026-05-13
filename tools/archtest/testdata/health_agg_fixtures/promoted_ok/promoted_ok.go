// Package promotedok embeds *base.FakeResource so that Checkers/Worker/Close
// reach the type via promoted methods. HEALTH-AGG-01 must NOT flag App.
package promotedok

import "healthaggfixtures/base"

type App struct {
	*base.FakeResource
}
