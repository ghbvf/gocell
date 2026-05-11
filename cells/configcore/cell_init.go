// cell_init.go hosts ConfigCore.initInternal() and the initXxxSlice helpers that
// construct the six slices during cell initialization. Constructor + options
// live in cell.go; Init() is generated in cell_gen.go.
package configcore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/cells/configcore/slices/configread"
	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/cells/configcore/slices/featureflag"
	"github.com/ghbvf/gocell/cells/configcore/slices/flagwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, health probes).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group and subscribe blocks. This is a permanent convention,
// not a transitional shim.
//
//nolint:unparam // ctx is a contract parameter; unused here, used by other cells
func (c *ConfigCore) initInternal(ctx context.Context, reg cell.Registry) error {
	clock.MustHaveClock(c.clk, "configcore.initInternal")

	if c.casProtocolNil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"configcore: typed-nil *cas.Protocol rejected; use cas.MustNewProtocol(cas.WithVersionField(\"version\")) in composition root")
	}
	if c.casProtocol == nil && reg.DurabilityMode() == cell.DurabilityDurable {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"configcore durable mode requires a CAS protocol; "+
				"use WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(\"version\"))) in composition root")
	}

	// WithInMemoryDefaults defers repo construction to here so c.clk is available.
	if c.useInMemoryDefaults {
		c.configRepo = mem.NewConfigRepository(c.clk)
		c.flagRepo = mem.NewFlagRepository(c.clk)
	}

	durabilityMode := reg.DurabilityMode()

	// deriveModes' PublishFailureMode return is retained for TestConfigCore_DeriveModes
	// (documents the demo→FailOpen / durable→FailClosed derivation at slice level);
	// the Cell-boundary DirectEmitter fail mode is now owned by the kernel helper.
	runMode, _ := c.deriveModes(durabilityMode)

	if err := c.resolveEmitter(durabilityMode); err != nil {
		return err
	}
	// resolveEmitter enforces the (OutboxWriter, TxRunner) pairing invariant
	// using the original c.txRunner; only after it succeeds do we install the
	// demoTxRunner fallback so slice constructors see a non-nil TxRunner.
	if c.txRunner == nil {
		c.logger.Warn("configcore: using cell.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = cell.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := cell.CheckNotNoop(durabilityMode, "configcore", c.txRunner); err != nil {
		return err
	}
	if err := c.ensureCursorCodec(reg); err != nil {
		return err
	}
	if err := c.initAllSlices(runMode); err != nil {
		return err
	}

	// Route groups and subscriptions removed: cell_gen.go owns Init and renders them.

	// Register health probes (emitter fail-open rate checker).
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
	}

	return nil
}

// initAllSlices constructs all 6 configcore slices.
func (c *ConfigCore) initAllSlices(runMode query.RunMode) error {
	if err := c.initWriteSlice(); err != nil {
		return err
	}
	if err := c.initReadSlice(runMode); err != nil {
		return err
	}
	if err := c.initPublishSlice(); err != nil {
		return err
	}
	c.initSubscribeSlice()
	if err := c.initFlagSlice(runMode); err != nil {
		return err
	}
	return c.initFlagWriteSlice()
}

// resolveEmitter delegates to cell.ResolveCellEmitter (mutual exclusion +
// WithEmitter durable guard + ResolveEmitter delegation + L2 non-durable
// warn) and clears the pending outbox dep fields.
//
// configcore uses DirectPublishFailClosed: config changes must propagate or
// the write surfaces an error so operators notice misconfig instead of
// running with a stale subscriber view. Per-entry FailurePolicy
// (outbox.Entry.FailurePolicy) lets individual topics opt into fail-open;
// configwrite uses the default.
func (c *ConfigCore) resolveEmitter(mode cell.DurabilityMode) error {
	outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
		EmitterConfig: cell.EmitterConfig{
			CellID:            "configcore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          c.txRunner,
			Logger:            c.logger,
			DirectPublishMode: outbox.DirectPublishFailClosed,
			MetricsProvider:   c.metricsProvider,
			Clock:             c.clk,
		},
		PreResolved:      c.emitter,
		ConsistencyLevel: c.ConsistencyLevel(),
	})
	if err != nil {
		return err
	}
	c.emitter = outcome.Emitter
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	return nil
}

// deriveModes is the single translation point from kernel/cell.DurabilityMode
// to run modes used by slices. Called only once at Init() time; propagated via
// constructor parameters (do not call in handler/repository).
//
// S10 MODE-SEMANTIC-SPLIT-01: read-path cursor tolerance (RunMode) and write-
// path publisher failure semantics (PublishFailureMode) are separate types that
// evolve independently.
//
// ref: Uber fx Provide/Decorate — each decision gets its own typed injection.
func (c *ConfigCore) deriveModes(durabilityMode cell.DurabilityMode) (query.RunMode, configpublish.PublishFailureMode) {
	demo := durabilityMode == cell.DurabilityDemo
	return query.RunModeForDemo(demo), configpublish.PublishFailureModeForDemo(demo)
}

// ensureCursorCodec sets a default cursor codec in demo mode or returns an
// error in durable mode when no codec was injected.
// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
func (c *ConfigCore) ensureCursorCodec(reg cell.Registry) error {
	if c.cursorCodec != nil {
		return nil
	}
	if reg.DurabilityMode() == cell.DurabilityDurable {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"configcore durable mode requires a cursor codec; "+
				"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
				"the built-in demo key is public in the source tree")
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-CONFIG-CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("configcore: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}

func (c *ConfigCore) initWriteSlice() error {
	opts := []configwrite.Option{configwrite.WithEmitter(c.emitter), configwrite.WithTxManager(c.txRunner)}
	writeSvc, err := configwrite.NewService(c.configRepo, c.logger, c.clk, opts...)
	if err != nil {
		return fmt.Errorf("configcore: init write slice: %w", err)
	}
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("configwrite", "configcore", cellvocab.L2))
	return nil
}

func (c *ConfigCore) initReadSlice(runMode query.RunMode) error {
	readSvc, err := configread.NewService(c.configRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("config-read: %w", err)
	}
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("configread", "configcore", cellvocab.L0))
	return nil
}

func (c *ConfigCore) initPublishSlice() error {
	opts := []configpublish.Option{
		configpublish.WithEmitter(c.emitter),
		configpublish.WithTxManager(c.txRunner),
	}
	publishSvc, err := configpublish.NewService(c.configRepo, c.logger, c.clk, opts...)
	if err != nil {
		return fmt.Errorf("configcore: init publish slice: %w", err)
	}
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("configpublish", "configcore", cellvocab.L2))
	return nil
}

func (c *ConfigCore) initSubscribeSlice() {
	c.subscribeSvc = configsubscribe.NewService(c.logger,
		configsubscribe.WithConfigEventCollector(c.configEventCollector),
	)
	c.AddSlice(cell.NewBaseSlice("configsubscribe", "configcore", cellvocab.L3))
}

func (c *ConfigCore) initFlagSlice(runMode query.RunMode) error {
	flagSvc, err := featureflag.NewService(c.flagRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("feature-flag: %w", err)
	}
	c.flagHandler = featureflag.NewHandler(flagSvc)
	c.AddSlice(cell.NewBaseSlice("featureflag", "configcore", cellvocab.L0))
	return nil
}

// initFlagWriteSlice registers the flag-write L1 slice: Create/Update/Toggle/Delete
// with local transaction only (no outbox emission).
func (c *ConfigCore) initFlagWriteSlice() error {
	opts := []flagwrite.Option{flagwrite.WithTxManager(c.txRunner)}
	flagWriteSvc, err := flagwrite.NewService(c.flagRepo, c.logger, c.clk, opts...)
	if err != nil {
		return fmt.Errorf("configcore: init flag-write slice: %w", err)
	}
	c.flagWriteHandler = flagwrite.NewHandler(flagWriteSvc)
	c.AddSlice(cell.NewBaseSlice("flagwrite", "configcore", cellvocab.L1))
	return nil
}
