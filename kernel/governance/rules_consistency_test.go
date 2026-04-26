package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// buildConsistencyProject creates a minimal ProjectMeta for CONTRACT-CONSISTENCY-EMIT-01 tests.
// ownerCell: the cell that owns the contracts.
// contracts: list of (id, consistencyLevel, triggers...) tuples.
func buildConsistencyProject(ownerCell string, contracts []*metadata.ContractMeta) *metadata.ProjectMeta {
	contractMap := map[string]*metadata.ContractMeta{}
	for _, c := range contracts {
		contractMap[c.ID] = c
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			ownerCell: {
				ID:               ownerCell,
				Type:             "core",
				ConsistencyLevel: "L2",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_test"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + ownerCell + ".startup"}},
				Dir:              ownerCell,
				File:             "cells/" + ownerCell + "/cell.yaml",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  contractMap,
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// httpContract creates a ContractMeta for an HTTP contract.
func httpContract(id, ownerCell, consistencyLevel string, triggers []string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:               id,
		Kind:             "http",
		OwnerCell:        ownerCell,
		ConsistencyLevel: consistencyLevel,
		Lifecycle:        "active",
		Triggers:         triggers,
		Endpoints: metadata.EndpointsMeta{
			Server:  ownerCell,
			Clients: []string{"edge-bff"},
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "POST",
				Path:          "/api/v1/test",
				SuccessStatus: 201,
			},
		},
		Dir:  "contracts/http/test/action/v1",
		File: "contracts/http/test/action/v1/contract.yaml",
	}
}

// writeDtoConst writes a Go file defining a topic constant in a dto package.
func writeDtoConst(t *testing.T, dir, constName, topicValue string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	src := "package dto\n\nconst " + constName + " = \"" + topicValue + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "topics.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write topics.go: %v", err)
	}
}

// writeServiceFile writes a Go service file with an emit call.
func writeServiceFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(content), 0o600); err != nil {
		t.Fatalf("write service.go: %v", err)
	}
}

// findResultByCode returns all ValidationResults matching the given code.
func findResultByCode(results []ValidationResult, code string) []ValidationResult {
	var out []ValidationResult
	for _, r := range results {
		if r.Code == code {
			out = append(out, r)
		}
	}
	return out
}

// TestCONTRACTCONSISTENCYEMIT01_CaseA: L2 contract + triggers + service emits → PASS.
func TestCONTRACTCONSISTENCYEMIT01_CaseA(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.created.v1"

	// Write dto topic constant.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestCreated", topic)

	// Write service.go that emits via outbox.Emit with dto.TopicTestCreated.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter) error {
	return outbox.Emit(ctx, e, dto.TopicTestCreated, struct{}{})
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	if got := findResultByCode(results, codeContractConsistencyEmit01); len(got) != 0 {
		t.Errorf("case A: expected 0 findings, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseB: triggers present + L1 → level mismatch error.
func TestCONTRACTCONSISTENCYEMIT01_CaseB(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.created.v1"

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L1", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)
	if len(got) != 1 {
		t.Fatalf("case B: expected 1 finding, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "triggers imply L2+") {
		t.Errorf("case B: expected 'triggers imply L2+' in message, got: %s", got[0].Message)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("case B: expected SeverityError, got %s", got[0].Severity)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseC: L2 contract + no triggers → required error.
func TestCONTRACTCONSISTENCYEMIT01_CaseC(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", nil),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)
	if len(got) != 1 {
		t.Fatalf("case C: expected 1 finding, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "must declare non-empty triggers") {
		t.Errorf("case C: expected 'must declare non-empty triggers' in message, got: %s", got[0].Message)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseD: contract trigger not emitted by service → forward check fail.
func TestCONTRACTCONSISTENCYEMIT01_CaseD(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	declaredTopic := "event.test.created.v1"
	emittedTopic := "event.test.other.v1"

	// Write dto with a different topic than what the contract declares.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestOther", emittedTopic)

	// Service emits a different topic.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter) error {
	return outbox.Emit(ctx, e, dto.TopicTestOther, struct{}{})
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", []string{declaredTopic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	// Expect: forward check fail (declared not emitted) + reverse check fail (emitted not declared).
	forwardFail := false
	reverseFail := false
	for _, r := range got {
		if strings.Contains(r.Message, "no non-test Go file") && strings.Contains(r.Message, declaredTopic) {
			forwardFail = true
		}
		if strings.Contains(r.Message, "service emits") && strings.Contains(r.Message, emittedTopic) {
			reverseFail = true
		}
	}
	if !forwardFail {
		t.Errorf("case D: expected forward check failure for %q, findings: %v", declaredTopic, got)
	}
	if !reverseFail {
		t.Errorf("case D: expected reverse check failure for %q, findings: %v", emittedTopic, got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseE: service emits topic not in any contract trigger → reverse check fail.
func TestCONTRACTCONSISTENCYEMIT01_CaseE(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	declaredTopic := "event.test.created.v1"
	extraTopic := "event.test.extra.v1"

	// Write dto with both topics.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	if err := os.MkdirAll(dtoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtoDir, "topics.go"), []byte(`package dto

const (
	TopicTestCreated = "`+declaredTopic+`"
	TopicTestExtra   = "`+extraTopic+`"
)
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Service emits both topics.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter) error {
	_ = outbox.Emit(ctx, e, dto.TopicTestCreated, struct{}{})
	return outbox.Emit(ctx, e, dto.TopicTestExtra, struct{}{})
}
`)

	// Contract only declares one trigger.
	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", []string{declaredTopic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	reverseFail := false
	for _, r := range got {
		if strings.Contains(r.Message, "service emits") && strings.Contains(r.Message, extraTopic) {
			reverseFail = true
		}
	}
	if !reverseFail {
		t.Errorf("case E: expected reverse check failure for %q, findings: %v", extraTopic, got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseF: dynamic topic in outbox.Emit → error.
func TestCONTRACTCONSISTENCYEMIT01_CaseF(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.created.v1"

	// dto has the constant.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestCreated", topic)

	// Service uses a dynamic topic via fmt.Sprintf in outbox.Emit.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"fmt"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter, v string) error {
	return outbox.Emit(ctx, e, fmt.Sprintf("event.x.%s", v), struct{}{})
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	dynamicFail := false
	relPathOK := false
	for _, r := range got {
		if strings.Contains(r.Message, "dynamic topic in emit") {
			dynamicFail = true
			// B4: file path in finding must be relative (not absolute).
			if r.File != "" && !strings.HasPrefix(r.File, "/") {
				relPathOK = true
			}
		}
	}
	if !dynamicFail {
		t.Errorf("case F: expected dynamic topic error, findings: %v", got)
	}
	if !relPathOK {
		t.Errorf("case F: dynamic topic finding must report a relative file path, findings: %v", got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_ReceiverEmitInlineCompLit verifies that the
// receiver-style emit with an inline composite literal is correctly resolved.
func TestCONTRACTCONSISTENCYEMIT01_ReceiverEmitInlineCompLit(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.done.v1"

	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestDone", topic)

	// Service uses receiver-style emit with inline composite literal.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

type emitter interface {
	Emit(ctx context.Context, entry outbox.Entry) error
}

func doEmit(ctx context.Context, e emitter) error {
	return e.Emit(ctx, outbox.Entry{EventType: dto.TopicTestDone})
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.done.v1", ownerCell, "L2", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	if got := findResultByCode(results, codeContractConsistencyEmit01); len(got) != 0 {
		t.Errorf("receiver-emit-inline: expected 0 findings, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_ReceiverEmitPreBuiltEntry verifies that a
// pre-built entry variable passed to receiver-style emit is resolved via
// the assignment walk-back.
func TestCONTRACTCONSISTENCYEMIT01_ReceiverEmitPreBuiltEntry(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.done.v1"

	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestDone", topic)

	// Service builds entry variable and then calls receiver emit.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

type emitter interface {
	Emit(ctx context.Context, entry outbox.Entry) error
}

func doEmit(ctx context.Context, e emitter) error {
	entry := outbox.Entry{EventType: dto.TopicTestDone}
	return e.Emit(ctx, entry)
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.done.v1", ownerCell, "L2", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	if got := findResultByCode(results, codeContractConsistencyEmit01); len(got) != 0 {
		t.Errorf("receiver-emit-prebuilt: expected 0 findings, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_IndirectHelper verifies that topic resolution
// works when the topic constant is passed as an argument to a helper method
// (collectAllTopicSelectors picks up dto.TopicXxx selectors anywhere in the file).
func TestCONTRACTCONSISTENCYEMIT01_IndirectHelper(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic := "event.test.created.v1"

	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	writeDtoConst(t, dtoDir, "TopicTestCreated", topic)

	// Service passes topic to a helper — dto.TopicTestCreated appears in the file
	// but not directly in an outbox.Emit or entry.EventType context.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func publish(ctx context.Context, e outbox.Emitter, topic string) error {
	return outbox.Emit(ctx, e, topic, struct{}{})
}

func doWork(ctx context.Context, e outbox.Emitter) error {
	return publish(ctx, e, dto.TopicTestCreated)
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.action.v1", ownerCell, "L2", []string{topic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()

	// outbox.Emit sees "topic" (an ident not in fileConsts) → not resolved directly.
	// But collectAllTopicSelectors picks up dto.TopicTestCreated from doWork.
	// Forward check should pass; no dynamic-topic error (the ident is not a call expr).
	dynamicErrors := 0
	for _, r := range findResultByCode(results, codeContractConsistencyEmit01) {
		if strings.Contains(r.Message, "dynamic topic") {
			dynamicErrors++
		}
	}
	if dynamicErrors > 0 {
		t.Errorf("indirect-helper: should not emit dynamic topic error for ident arg, got %d dynamic errors", dynamicErrors)
	}

	// The forward check should pass because collectAllTopicSelectors found the topic.
	forwardErrors := 0
	for _, r := range findResultByCode(results, codeContractConsistencyEmit01) {
		if strings.Contains(r.Message, "no non-test Go file") {
			forwardErrors++
		}
	}
	if forwardErrors > 0 {
		t.Errorf("indirect-helper: forward check should pass via selector scan, got %d errors: %v",
			forwardErrors, findResultByCode(results, codeContractConsistencyEmit01))
	}
}

// TestCONTRACTCONSISTENCYEMIT01_ExamplesSkipped verifies that contracts under
// examples/ are not checked by this rule.
func TestCONTRACTCONSISTENCYEMIT01_ExamplesSkipped(t *testing.T) {
	root := t.TempDir()

	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"ordercell": {
				ID:               "ordercell",
				Type:             "core",
				ConsistencyLevel: "L2",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "examples", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "orders"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.ordercell.startup"}},
				Dir:              "ordercell",
				File:             "examples/todoorder/cells/ordercell/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.order.create.v1": {
				ID:               "http.order.create.v1",
				Kind:             "http",
				OwnerCell:        "ordercell",
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Triggers:         nil, // no triggers — would fail if not skipped
				Endpoints: metadata.EndpointsMeta{
					Server:  "ordercell",
					Clients: []string{"edge-bff"},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/orders/",
						SuccessStatus: 201,
					},
				},
				Dir:  "examples/todoorder/contracts/http/order/create/v1",
				File: "examples/todoorder/contracts/http/order/create/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	if got := findResultByCode(results, codeContractConsistencyEmit01); len(got) != 0 {
		t.Errorf("examples-skipped: expected 0 findings, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_SubscriberTopicNotCollected is a regression test
// for A2: collectAllTopicSelectors must NOT be called for files that contain no real
// emit call sites. A slice that only subscribes to dto.TopicB must not contribute
// dto.TopicB to the cell's emit-topic set.
func TestCONTRACTCONSISTENCYEMIT01_SubscriberTopicNotCollected(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topicA := "event.test.a.v1"
	topicB := "event.test.b.v1"

	// dto has both constants.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	if err := os.MkdirAll(dtoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtoDir, "topics.go"), []byte("package dto\n\nconst (\n\tTopicA = \""+topicA+"\"\n\tTopicB = \""+topicB+"\"\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// emitter-slice: emits TopicA via outbox.Emit.
	emitterDir := filepath.Join(root, "cells", ownerCell, "slices", "emitterslice")
	writeServiceFile(t, emitterDir, `package emitterslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter) error {
	return outbox.Emit(ctx, e, dto.TopicA, struct{}{})
}
`)

	// subscriber-slice: subscribes to TopicB — but does NOT emit. Uses dto.TopicB only
	// in a subscribe call. This file must NOT contribute TopicB to emit topics.
	subDir := filepath.Join(root, "cells", ownerCell, "slices", "subscriberslice")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "service.go"), []byte(`package subscriberslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
)

type handler func(ctx context.Context, topic string) error

func register(sub handler) {
	sub(context.Background(), dto.TopicB)
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Contract declares only TopicA as trigger (L2).
	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.a.v1", ownerCell, "L2", []string{topicA}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	// TopicB must NOT appear in emit topics — no reverse finding for it.
	for _, r := range got {
		if strings.Contains(r.Message, topicB) {
			t.Errorf("SubscriberTopicNotCollected: dto.TopicB from subscriber-only file must not appear in emit topics; finding: %v", r)
		}
	}
	// TopicA is correctly emitted and declared — no forward/reverse errors for it.
	if len(got) != 0 {
		t.Errorf("SubscriberTopicNotCollected: expected 0 findings, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseE_ReverseMismatch is the existing CaseE renamed
// to clarify it tests the reverse-mismatch (emit without declaration) path.
// The original CaseE function is preserved under its original name for git history.

// TestCONTRACTCONSISTENCYEMIT01_CaseE_NoOpPassthrough: L0 contract, no triggers, no
// service.go for the cell → 0 ValidationResults (bottom-out path that was missing).
func TestCONTRACTCONSISTENCYEMIT01_CaseE_NoOpPassthrough(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"

	// L0 HTTP contract — no triggers, no cells/<ownerCell>/slices directory.
	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.noop.v1", ownerCell, "L0", nil),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)
	if len(got) != 0 {
		t.Errorf("CaseE_NoOpPassthrough: expected 0 findings for L0 no-triggers, got %d: %v", len(got), got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_CaseD_ReceiverStyle: receiver-style Emit emits TopicX,
// contract declares TopicY. Expects both forward (declared Y but no emit Y) and
// reverse (emit X but no declare X) findings.
func TestCONTRACTCONSISTENCYEMIT01_CaseD_ReceiverStyle(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	declaredTopic := "event.y.v1"
	emittedTopic := "event.x.v1"

	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	if err := os.MkdirAll(dtoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtoDir, "topics.go"), []byte("package dto\n\nconst (\n\tTopicX = \""+emittedTopic+"\"\n\tTopicY = \""+declaredTopic+"\"\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

type emitter interface {
	Emit(ctx context.Context, entry outbox.Entry) error
}

func doEmit(ctx context.Context, e emitter) error {
	return e.Emit(ctx, outbox.Entry{EventType: dto.TopicX})
}
`)

	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.y.v1", ownerCell, "L2", []string{declaredTopic}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	forwardFail := false
	reverseFail := false
	for _, r := range got {
		if strings.Contains(r.Message, "no non-test Go file") && strings.Contains(r.Message, declaredTopic) {
			forwardFail = true
		}
		if strings.Contains(r.Message, "service emits") && strings.Contains(r.Message, emittedTopic) {
			reverseFail = true
		}
	}
	if !forwardFail {
		t.Errorf("CaseD_ReceiverStyle: expected forward failure for %q, findings: %v", declaredTopic, got)
	}
	if !reverseFail {
		t.Errorf("CaseD_ReceiverStyle: expected reverse failure for %q, findings: %v", emittedTopic, got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_MultiContractNoDuplicateFindings: same ownerCell has
// 2 L2 HTTP contracts each declaring 1 trigger; service emits a 3rd topic not in either
// contract. Assert the reverse finding for the 3rd topic appears exactly once.
func TestCONTRACTCONSISTENCYEMIT01_MultiContractNoDuplicateFindings(t *testing.T) {
	root := t.TempDir()
	ownerCell := "testcell"
	topic1 := "event.test.one.v1"
	topic2 := "event.test.two.v1"
	extraTopic := "event.test.extra.v1"

	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	if err := os.MkdirAll(dtoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtoDir, "topics.go"), []byte("package dto\n\nconst (\n\tTopicOne   = \""+topic1+"\"\n\tTopicTwo   = \""+topic2+"\"\n\tTopicExtra = \""+extraTopic+"\"\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "testslice")
	writeServiceFile(t, sliceDir, `package testslice

import (
	"context"
	"github.com/ghbvf/gocell/cells/testcell/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
)

func doEmit(ctx context.Context, e outbox.Emitter) error {
	_ = outbox.Emit(ctx, e, dto.TopicOne, struct{}{})
	_ = outbox.Emit(ctx, e, dto.TopicTwo, struct{}{})
	return outbox.Emit(ctx, e, dto.TopicExtra, struct{}{})
}
`)

	// Two L2 contracts, each declaring one of the two topics.
	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.test.one.v1", ownerCell, "L2", []string{topic1}),
		httpContract("http.test.two.v1", ownerCell, "L2", []string{topic2}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	extraCount := 0
	for _, r := range got {
		if strings.Contains(r.Message, "service emits") && strings.Contains(r.Message, extraTopic) {
			extraCount++
		}
	}
	if extraCount != 1 {
		t.Errorf("MultiContractNoDuplicateFindings: expected exactly 1 reverse finding for %q, got %d; findings: %v",
			extraTopic, extraCount, got)
	}
}

// TestCONTRACTCONSISTENCYEMIT01_SubscriberSelectorIgnored is a regression test
// verifying that a subscribe(ctx, dto.TopicY, h) call and a comparison
// `if topic == dto.TopicZ {}` in a service file do NOT contribute their topic
// selectors to the cell's emit-topic set. Only actual outbox.Emit calls (or
// receiver *.Emit calls with an outbox.Entry) count as emit evidence.
//
// Setup:
//   - Cell "foo" has one HTTP contract with consistencyLevel: L2 and
//     triggers: [event.foo.y.v1].
//   - The cell's service.go subscribes to dto.TopicY and compares dto.TopicZ
//     but contains NO outbox.Emit call to either topic.
//
// Expected: forward-check fails because nothing actually emits event.foo.y.v1.
func TestCONTRACTCONSISTENCYEMIT01_SubscriberSelectorIgnored(t *testing.T) {
	root := t.TempDir()
	ownerCell := "foo"
	topicY := "event.foo.y.v1"
	topicZ := "event.foo.z.v1"

	// Write dto with both constants.
	dtoDir := filepath.Join(root, "cells", ownerCell, "internal", "dto")
	if err := os.MkdirAll(dtoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dtoDir, "topics.go"), []byte(
		"package dto\n\nconst (\n\tTopicY = \""+topicY+"\"\n\tTopicZ = \""+topicZ+"\"\n)\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	// handler/service.go: subscribes to dto.TopicY and compares dto.TopicZ —
	// but contains NO outbox.Emit call to either.
	sliceDir := filepath.Join(root, "cells", ownerCell, "slices", "handler")
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sliceDir, "service.go"), []byte(`package handler

import (
	"context"
	"github.com/ghbvf/gocell/cells/foo/internal/dto"
)

type subscribeFn func(ctx context.Context, topic string, handler func()) error

func register(ctx context.Context, sub subscribeFn) error {
	return sub(ctx, dto.TopicY, func() {})
}

func check(topic string) bool {
	return topic == dto.TopicZ
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Contract declares trigger event.foo.y.v1 (L2).
	project := buildConsistencyProject(ownerCell, []*metadata.ContractMeta{
		httpContract("http.foo.x.v1", ownerCell, "L2", []string{topicY}),
	})

	v := NewValidator(project, root)
	results := v.validateCONTRACTCONSISTENCYEMIT01()
	got := findResultByCode(results, codeContractConsistencyEmit01)

	// Forward check must fail: no file actually emits event.foo.y.v1.
	forwardFail := false
	for _, r := range got {
		if strings.Contains(r.Message, "no non-test Go file") && strings.Contains(r.Message, topicY) {
			forwardFail = true
		}
	}
	if !forwardFail {
		t.Errorf("SubscriberSelectorIgnored: expected forward-check failure for %q (subscriber/comparison must not count as emit); findings: %v",
			topicY, got)
	}

	// TopicZ must NOT appear as an emitted topic (no reverse finding for it).
	for _, r := range got {
		if strings.Contains(r.Message, topicZ) {
			t.Errorf("SubscriberSelectorIgnored: dto.TopicZ from comparison-only code must not appear in emit topics; finding: %v", r)
		}
	}
}
