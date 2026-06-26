package prowlarr

import (
	"context"

	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/worker"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Config   Config
	Importer lazy.Lazy[importer.Importer]
	Logger   *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Worker     worker.Worker `group:"workers"`
	CrawlNowFn CrawlNowFunc  `name:"prowlarr_crawl_now"`
}

func New(p Params) Result {
	c := &crawler{
		config:      p.Config,
		client:      newClient(p.Config.URL, p.Config.APIKey),
		imp:         p.Importer,
		logger:      p.Logger.Named("prowlarr"),
		triggerChan: make(chan int, 10),
		stopped:     make(chan struct{}),
	}

	return Result{
		Worker: worker.NewWorker(
			"prowlarr_crawler",
			fx.Hook{
				OnStart: func(ctx context.Context) error {
					if p.Config.APIKey == "" {
						p.Logger.Named("prowlarr").Info("prowlarr: no API key configured, crawler disabled")
						return nil
					}
					go c.start(ctx)
					return nil
				},
				OnStop: func(context.Context) error {
					// Guard against double-close if OnStop is called before OnStart
					select {
					case <-c.stopped:
					default:
						close(c.stopped)
					}
					return nil
				},
			},
		),
		CrawlNowFn: func(indexerID int) {
			select {
			case c.triggerChan <- indexerID:
			default:
				// Channel full — drop; caller can retry
			}
		},
	}
}
