package accesscore

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

const (
	// defaultAccessCoreRefreshGCStartTimeout is the lifecycle hook start timeout
	// for the refresh GC worker.
	defaultAccessCoreRefreshGCStartTimeout = 5 * time.Second
	// defaultAccessCoreRefreshGCStopTimeout is the lifecycle hook stop timeout
	// for the refresh GC worker.
	defaultAccessCoreRefreshGCStopTimeout = 5 * time.Second
)

func (c *AccessCore) refreshGCHook() cell.LifecycleHook {
	return cell.LifecycleHook{
		Name:         "accesscore.refresh-gc",
		StartTimeout: defaultAccessCoreRefreshGCStartTimeout,
		StopTimeout:  defaultAccessCoreRefreshGCStopTimeout,
		OnStart: func(ctx context.Context) error {
			worker, err := refresh.NewGCWorker(refresh.GCWorkerConfig{
				Store:     c.refreshStore,
				Clock:     realClock{},
				Interval:  c.refreshGCInterval,
				Retention: c.refreshGCRetention,
				Logger:    c.logger,
				Metrics:   c.refreshGCCollector,
			})
			if err != nil {
				return err
			}
			c.refreshGC = worker
			return worker.Start(ctx)
		},
		OnStop: func(ctx context.Context) error {
			if c.refreshGC == nil {
				return nil
			}
			err := c.refreshGC.Stop(ctx)
			if err == nil {
				c.refreshGC = nil
			}
			return err
		},
	}
}
