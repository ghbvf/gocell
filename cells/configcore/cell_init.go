// cell_init.go hosts ConfigCore.Init() and the initXxxSlice helpers that
// construct the six slices during cell initialization. Runtime request-path
// code lives in cell_routes.go; constructor + options live in cell.go.
package configcore

import (
	"context"
	"fmt"
	"log/slog"

	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/cells/configcore/slices/configread"
	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/cells/configcore/slices/featureflag"
	"github.com/ghbvf/gocell/cells/configcore/slices/flagwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Init constructs all slices and registers them. If pgPool is set
// (via WithPostgresPool), the PG repos are built here after all options
// have been applied (so WithKeyProvider can precede or follow WithPostgresPool).
func (c *ConfigCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Deferred PG repo construction — options are all applied before Init().
	if c.pgPool != nil && c.configRepo == nil {
		session := cellpg.NewSession(c.pgPool)
		var repoOpts []cellpg.ConfigRepoOption
		if c.staleCipherCounter != nil {
			repoOpts = append(repoOpts, cellpg.WithOnStaleCipher(func(_, _, _ string) {
				c.staleCipherCounter.Inc()
			}))
		}
		c.configRepo = cellpg.NewConfigRepository(session, c.valueTransformer, nil, repoOpts...)
		c.flagRepo = cellpg.NewFlagRepository(session)
	}

	// deriveModes' PublishFailureMode return is retained for TestConfigCore_DeriveModes
	// (documents the demo→FailOpen / durable→FailClosed derivation at slice level);
	// the Cell-boundary DirectEmitter fail mode is now owned by the kernel helper.
	runMode, _ := c.deriveModes(deps.DurabilityMode)

	if err := c.resolveEmitter(deps.DurabilityMode); err != nil {
		return err
	}

	if err := c.ensureCursorCodec(deps); err != nil {
		return err
	}

	c.initWriteSlice()
	if err := c.initReadSlice(runMode); err != nil {
		return err
	}
	c.initPublishSlice()
	c.initSubscribeSlice()
	if err := c.initFlagSlice(runMode); err != nil {
		return err
	}
	if err := c.initFlagWriteSlice(); err != nil {
		return err
	}
	return nil
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
	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
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
func (c *ConfigCore) ensureCursorCodec(deps cell.Dependencies) error {
	if c.cursorCodec != nil {
		return nil
	}
	if deps.DurabilityMode == cell.DurabilityDurable {
		return errcode.New(errcode.ErrCellMissingCodec,
			"configcore durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
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

func (c *ConfigCore) initWriteSlice() {
	opts := []configwrite.Option{configwrite.WithEmitter(c.emitter), configwrite.WithTxManager(c.txRunner)}
	writeSvc := configwrite.NewService(c.configRepo, c.logger, opts...)
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("configwrite", "configcore", cell.L2))
}

func (c *ConfigCore) initReadSlice(runMode query.RunMode) error {
	readSvc, err := configread.NewService(c.configRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("config-read: %w", err)
	}
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("configread", "configcore", cell.L0))
	return nil
}

func (c *ConfigCore) initPublishSlice() {
	opts := []configpublish.Option{
		configpublish.WithEmitter(c.emitter),
		configpublish.WithTxManager(c.txRunner),
	}
	publishSvc := configpublish.NewService(c.configRepo, c.logger, opts...)
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("configpublish", "configcore", cell.L2))
}

func (c *ConfigCore) initSubscribeSlice() {
	c.subscribeSvc = configsubscribe.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("configsubscribe", "configcore", cell.L3))
}

func (c *ConfigCore) initFlagSlice(runMode query.RunMode) error {
	flagSvc, err := featureflag.NewService(c.flagRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("feature-flag: %w", err)
	}
	c.flagHandler = featureflag.NewHandler(flagSvc)
	c.AddSlice(cell.NewBaseSlice("featureflag", "configcore", cell.L0))
	return nil
}

// initFlagWriteSlice registers the flag-write L2 slice: Create/Update/Toggle/Delete
// with transactional outbox (flag.changed.v1 event).
func (c *ConfigCore) initFlagWriteSlice() error {
	opts := []flagwrite.Option{flagwrite.WithEmitter(c.emitter), flagwrite.WithTxManager(c.txRunner)}
	flagWriteSvc, err := flagwrite.NewService(c.flagRepo, c.logger, opts...)
	if err != nil {
		return fmt.Errorf("configcore: init flag-write slice: %w", err)
	}
	c.flagWriteHandler = flagwrite.NewHandler(flagWriteSvc)
	c.AddSlice(cell.NewBaseSlice("flagwrite", "configcore", cell.L2))
	return nil
}
