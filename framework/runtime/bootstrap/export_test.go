package bootstrap

// export_test.go — white-box test helpers exported only during test compilation.
// Follows the Go convention: file name ends in _test.go; package is the
// non-test package (package bootstrap, not package bootstrap_test) so we can
// access unexported identifiers.
