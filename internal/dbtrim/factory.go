package dbtrim

import (
	"context"

	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/worker"
	"go.uber.org/fx"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type Params struct {
	fx.In
	Config Config
	DB     lazy.Lazy[*gorm.DB]
	Logger *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Worker worker.Worker `group:"workers"`
}

func New(p Params) Result {
	w := &trimWorker{
		config:  p.Config,
		db:      p.DB,
		logger:  p.Logger.Named("db_trim"),
		stopped: make(chan struct{}),
	}

	return Result{
		Worker: worker.NewWorker(
			"db_trim",
			fx.Hook{
				OnStart: func(ctx context.Context) error {
					if !p.Config.Enabled {
						p.Logger.Named("db_trim").Info("db_trim: disabled in config")
						return nil
					}
					go w.start(ctx)
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
	}
}
