// Negative-control fixture for ROOT-MODULE-NO-REPLACE-01
// (tools/archtest/root_module_no_replace_test.go). This go.mod deliberately
// carries one replace and one exclude directive so the reverse self-check can
// prove gomodutil.ReadReplaceExclude flags both. It is an isolated module (not in
// go.work) under testdata/, so no Go tooling builds it.
module example.com/rootreplacefixture

go 1.25

require example.com/other v1.0.0

replace example.com/other => ./local-other

exclude example.com/excluded v1.2.3
