// Package goose is a stand-in for github.com/pressly/goose/v3 used by the
// goose_session_locker archtest fixtures. The production scanner matches by
// resolved *types.Func.Pkg().Path(); fixtures can therefore use any package
// path as long as the test passes the same path into the predicate.
package goose

// Provider is a stand-in for goose.Provider.
type Provider struct{}

// SessionLocker is a stand-in for goose.SessionLocker.
type SessionLocker interface{ Lock() }

// noopLocker satisfies SessionLocker.
type noopLocker struct{}

func (noopLocker) Lock() {}

// NewLocker returns a stand-in SessionLocker.
func NewLocker() SessionLocker { return noopLocker{} }

// ProviderOption is a stand-in for goose.ProviderOption.
type ProviderOption func()

// WithSessionLocker is the locker option matched by the archtest.
func WithSessionLocker(SessionLocker) ProviderOption { return func() {} }

// WithTableName is an unrelated option used to test rule discrimination.
func WithTableName(string) ProviderOption { return func() {} }

// NewProvider is the constructor matched by the archtest.
func NewProvider(opts ...ProviderOption) (*Provider, error) {
	for _, o := range opts {
		o()
	}
	return &Provider{}, nil
}
