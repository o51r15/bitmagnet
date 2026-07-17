package rssfeed

import (
	"context"

	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/worker"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type Params struct {
	fx.In
	Config   Config
	DB       lazy.Lazy[*gorm.DB]
	Importer lazy.Lazy[importer.Importer]
	Logger   *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Worker    worker.Worker `group:"workers"`
	PollNowFn PollNowFunc  `name:"rssfeed_poll_now"`
}

func New(p Params) Result {
	pl := newPoller(p.Config, p.DB, p.Importer, p.Logger.Named("rssfeed"))

	return Result{
		Worker: worker.NewWorker(
			"rssfeed_poller",
			fx.Hook{
				OnStart: func(ctx context.Context) error {
					if len(p.Config.Feeds) == 0 {
						p.Logger.Named("rssfeed").Info("rssfeed: no feeds configured, poller disabled")
						return nil
					}
					hasEnabled := false
					for _, f := range p.Config.Feeds {
						if f.Enabled {
							hasEnabled = true
							break
						}
					}
					if !hasEnabled {
						p.Logger.Named("rssfeed").Info("rssfeed: no enabled feeds, poller disabled")
						return nil
					}
					go pl.start(ctx)
					return nil
				},
				OnStop: func(context.Context) error {
					select {
					case <-pl.stopped:
					default:
						close(pl.stopped)
					}
					return nil
				},
			},
		),
		PollNowFn: func(feedName string) {
			select {
			case pl.triggerChan <- feedName:
			default:
			}
		},
	}
}
