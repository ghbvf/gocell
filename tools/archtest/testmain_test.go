// INVARIANT: ARCHTEST-TESTMAIN-01

package archtest

import (
	"log/slog"
	"os"
	"strings"
	"testing"
)

// TestMain 在所有 Test* 跑前预热 typeseval.SharedResolver 的最高频
// cacheKey: (modRoot, tests=false, tags=nil, "./...") = LoadProductionPackages
// 主路径。覆盖 LAYER-* / 多数业务 Test* 的首次 SharedResolver 调用，把
// "首次 cache miss" 时机从 borderline test 的 wall 内移到 shard startup 外，
// 让 TestUserRepoConformanceEnrollment 等 borderline test 不再跨 20s
// slowgate budget。
//
// 为什么是 1 个 key 而不是多个：探索阶段实测分布显示 ./... 占 16.5%，
// subpath patterns (./cells/.../, ./cmd/.../, ./runtime/.../) 占 83.5% 但
// 每个 subpath 各自只被 1-3 个 Test 使用，预热成本 > 收益。其他高频
// cacheKey 如 SharedResolver(root, true, nil, "./tools/archtest/...")
// 会撞 PASS-FUNNEL-LOADPACKAGES-01（archtest *_test.go 禁直调
// SharedResolver），testmain_test.go 不是 funnel 实现无 exempt 资格。
//
// 扩 N 个 cacheKey 受 GHA 7GB shard 限制阻塞：closed PR #865 实证
// Tests:true + FlatNonDefaultTags cacheKey 单独就有 (false, nil) 的 2-3
// 倍 RSS，N=2 在 packages.Load 期间 peak 6-9GB 撞 OOM SIGTERM。详见
// issue #860 评论历史与 ADR 202605190000 §"Trigger evidence (closed PR #865)"
// (尚未落地，issue 维持 flag-cond)。
//
// fail-fast os.Exit(1)：packages.Load 在 TestMain 失败 = 后续所有 Test*
// 首次 SharedResolver 必失败；提前暴露避免 ~300 个测试函数逐一输出失败
// 再超时的诊断噪音。
//
// list-mode 早退：`go test -list <regex>` 只枚举测试名不执行任何 Test*，
// 因此 warmup 没必要也不应付。TestArchtestVerifyCoverage01 内部用
// `go test -list` K=4 个子进程 enumerate shard 分配；若子进程也跑 warmup，
// 子进程持有 1× 全模块 *types.Info RSS 与父 shard 已有 RSS 累积，K=4 路
// 同时驻留会撞 GHA 7GB shard 限制 → subprocess "signal: killed"。
// `-test.list` 检测在 flag.Parse 之前必须扫 os.Args（testing.M.Run 内部
// 才 parse flag），接受 `-test.list <pattern>` 与 `-test.list=<pattern>`
// 双形态及 `--test.list` 双 dash 变体。
//
// 该早退即使在当前 1-key warmup 下也是必要的（子进程的 1× 全模块 RSS +
// 父 shard 已有 RSS + GHA 其他开销，K=4 子进程合并仍可能逼近 7GB），且作
// 为未来扩 N warmup 的 defense in depth — closed PR #865 commit c84a6535b
// 实证：未做 list-mode 早退时，2-key warmup + K=4 子进程会 OOM；做了早退
// 后子进程不持有 warmup RSS，2-key 父 shard 仍 OOM（说明 list-mode 早退
// 与 warmup 扩 N 是两个独立维度的修复，本 PR 仅落地前者）。
//
// 无 escape hatch：按"不引入双路径"原则。若实测某 shard 退化按 trigger
// 单独改造，不预留逃生口。
//
// typeseval.LoadProductionPackages 调用封装在 warm.go 的 warmProductionPackages
// 中，与本文件隔离，以避免 PASS-FUNNEL-LOADPACKAGES-01 扫描 _test.go 文件
// 时检测到直接调用 typeseval 内部符号。
//
// ref: ADR docs/architecture/202605190000-adr-archtest-in-process-warmup.md
// ref: golangci-lint pkg/goanalysis/runner.go union load mode
func TestMain(m *testing.M) {
	if isListMode(os.Args) {
		// -list mode: skip warmup; m.Run() with -test.list only enumerates names.
		// Do NOT add slog output in this branch: list-mode stdout is consumed by
		// `grep '^Test'` in verify-archtest.sh; any non-test-name line breaks the
		// discovery filter and can collapse shard distribution.
		os.Exit(m.Run())
	}
	root, err := lookupModuleRoot()
	if err != nil {
		slog.Error("archtest TestMain: lookupModuleRoot", "err", err,
			"cwd_resolution", "getwd-or-walkup-failure")
		os.Exit(1)
	}
	modPath, err := moduleImportPath(root)
	if err != nil {
		slog.Error("archtest TestMain: moduleImportPath", "err", err, "modRoot", root)
		os.Exit(1)
	}
	slog.Info("archtest TestMain: warming SharedResolver (may take 15-25s on cold run)")
	if err := warmProductionPackages(root, modPath); err != nil {
		slog.Error("archtest TestMain: warm", "err", err, "modRoot", root, "modPath", modPath)
		os.Exit(1)
	}
	slog.Info("archtest TestMain: warm-up complete")
	os.Exit(m.Run())
}

// isListMode reports whether os.Args contains `-test.list` with a non-empty
// pattern value. Detection mirrors Go testing.M.Run semantics
// (src/testing/testing.go: `if *matchList != "" { listTests(...) }`) — empty
// value means tests still execute and warmup is required.
//
// Accepted forms (all four mapped to "non-empty value" rule):
//   - `-test.list <pat>`   (space-separated; value = args[i+1])
//   - `-test.list=<pat>`   (equals-separated; value = suffix)
//   - `--test.list <pat>`  (double-dash; same as space-separated)
//   - `--test.list=<pat>`  (double-dash equals)
//
// Detection at TestMain entry pre-dates flag.Parse, so we scan args directly
// while preserving flag.Parse's stop rules: `--` and the first non-flag
// argument end flag parsing.
//
// Note: the Go toolchain (`go test -list`) translates to `-test.list <pat>`
// (single dash, space-separated) in the binary's os.Args; the `--test.list`
// double-dash variants are included as defensive coverage for direct
// test-binary invocation scenarios where the user follows the Go flag pkg
// convention of accepting both prefixes.
//
// Empty-value cases (return false, warmup runs):
//   - `-test.list=`          (equals with empty suffix — malformed but seen)
//   - `-test.list ""`        (space with literal empty arg)
//   - `-test.list` (at EOF)  (no value at all)
//
// These cases match Go testing's behavior of NOT entering list mode when
// matchList is empty, so warmup must still run to keep the actual test
// execution path within slowgate budget.
func isListMode(args []string) bool {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
			return false
		}

		name, value, hasValue := splitFlagArg(arg)
		if name == "test.list" {
			if hasValue {
				if value != "" {
					return true
				}
				continue
			}
			// Space-separated: value is the next arg if present and non-empty.
			if i+1 < len(args) {
				i++
				if args[i] != "" {
					return true
				}
			}
			continue
		}
		if !hasValue && testFlagTakesValue(name) && i+1 < len(args) {
			i++
		}
	}
	return false
}

func splitFlagArg(arg string) (name string, value string, hasValue bool) {
	if strings.HasPrefix(arg, "--") {
		arg = strings.TrimPrefix(arg, "--")
	} else {
		arg = strings.TrimPrefix(arg, "-")
	}
	name, value, hasValue = strings.Cut(arg, "=")
	return name, value, hasValue
}

func testFlagTakesValue(name string) bool {
	switch name {
	case "test.bench",
		"test.benchtime",
		"test.blockprofile",
		"test.blockprofilerate",
		"test.coverprofile",
		"test.cpu",
		"test.cpuprofile",
		"test.fuzz",
		"test.fuzzcachedir",
		"test.gocoverdir",
		"test.list",
		"test.memprofile",
		"test.memprofilerate",
		"test.mutexprofile",
		"test.mutexprofilefraction",
		"test.outputdir",
		"test.parallel",
		"test.run",
		"test.shuffle",
		"test.skip",
		"test.testlogfile",
		"test.timeout",
		"test.trace":
		return true
	default:
		return false
	}
}

// TestIsListMode locks isListMode semantics so future refactors can't drop
// -test.list detection silently. Closed PR #865 commit c84a6535b proved that
// dropping subprocess `go test -list` warmup is required to keep
// TestArchtestVerifyCoverage01 within GHA shard RSS budget, even under the
// current 1-key TestMain warmup (and as a defense-in-depth bar against future
// warmup expansion regressions).
func TestIsListMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no_test_args", args: []string{"binary"}, want: false},
		{name: "run_only", args: []string{"binary", "-test.run", "TestFoo"}, want: false},
		{name: "run_before_list", args: []string{"binary", "-test.run", "TestFoo", "-test.list", "^Test"}, want: true},
		{name: "boolean_flag_before_list", args: []string{"binary", "-test.v", "-test.list", "^Test"}, want: true},
		{name: "list_space_separated", args: []string{"binary", "-test.list", "^Test"}, want: true},
		{name: "list_equals_separated", args: []string{"binary", "-test.list=^Test"}, want: true},
		{name: "double_dash_space", args: []string{"binary", "--test.list", "^Test"}, want: true},
		{name: "double_dash_equals", args: []string{"binary", "--test.list=^Test"}, want: true},
		{name: "list_with_run_after", args: []string{"binary", "-test.list", "^Test", "-test.run", "TestFoo"}, want: true},
		// Empty-value forms: Go testing.M.Run gates listTests on *matchList != "".
		// isListMode mirrors that — empty value means tests still execute,
		// warmup must run.
		{name: "list_equals_empty_value", args: []string{"binary", "-test.list="}, want: false},
		{name: "list_space_empty_value", args: []string{"binary", "-test.list", ""}, want: false},
		{name: "list_no_value_at_eof", args: []string{"binary", "-test.list"}, want: false},
		{name: "double_dash_equals_empty", args: []string{"binary", "--test.list="}, want: false},
		{name: "empty_list_value_then_nonempty", args: []string{"binary", "-test.list", "", "-test.list", "^Test"}, want: true},
		{name: "duplicate_list_flag", args: []string{"binary", "-test.list", "A", "-test.list", "B"}, want: true},
		{name: "terminator_before_list", args: []string{"binary", "--", "-test.list", "^Test"}, want: false},
		{name: "single_dash_before_list", args: []string{"binary", "-", "-test.list", "^Test"}, want: false},
		{name: "non_flag_before_list", args: []string{"binary", "positional", "-test.list", "^Test"}, want: false},
		{name: "near_match_not_list", args: []string{"binary", "-test.listfoo"}, want: false},
		{name: "near_match_equals", args: []string{"binary", "-test.listfoo=bar"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isListMode(tc.args); got != tc.want {
				t.Fatalf("isListMode(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
