package accesscore

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

func (c *AccessCore) refreshGCHook() cell.LifecycleHook {
	return cell.LifecycleHook{
		Name:         "accesscore.refresh-gc",
		StartTimeout: 5 * time.Second,
		StopTimeout:  5 * time.Second,
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
