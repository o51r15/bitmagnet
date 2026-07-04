package seedlookup

import (
	"context"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/client"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
	workerPkg "github.com/bitmagnet-io/bitmagnet/internal/worker"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type Params struct {
	fx.In
	Config           Config
	KTable           ktable.Table
	Client           lazy.Lazy[client.Client]
	DB               lazy.Lazy[*gorm.DB]
	DhtCrawlerActive *concurrency.AtomicValue[bool] `name:"dht_crawler_active"`
	Logger           *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Worker   workerPkg.Worker `group:"workers"`
	HotQueue chan protocol.ID  `name:"seed_lookup_hot_queue"`
}

func New(p Params) Result {
	hotQueue := make(chan protocol.ID, hotQueueCap)

	w := &worker{
		config:           p.Config,
		kTable:           p.KTable,
		db:               p.DB,
		dhtCrawlerActive: p.DhtCrawlerActive,
		logger:           p.Logger.Named("seed_lookup"),
		stopped:          make(chan struct{}),
		hotQueue:         hotQueue,
		backfillQueue:    make(chan protocol.ID, 10),
	}

	return Result{
		Worker: workerPkg.NewWorker(
			"seed_lookup",
			fx.Hook{
				OnStart: func(ctx context.Context) error {
					if !p.Config.Enabled {
						p.Logger.Named("seed_lookup").Info("seed_lookup: disabled")
						return nil
					}
					cl, err := p.Client.Get()
					if err != nil {
						return err
					}
					w.client = cl
					go w.start()
					return nil
				},
				OnStop: func(context.Context) error {
					select {
					case <-w.stopped:
					default:
						close(w.stopped)
					}
					return nil
				},
			},
		),
		HotQueue: hotQueue,
	}
}
