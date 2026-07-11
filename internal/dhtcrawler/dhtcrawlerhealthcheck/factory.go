package dhtcrawlerhealthcheck

import (
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/dhtcrawler"
	"github.com/bitmagnet-io/bitmagnet/internal/health"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/server"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	Config                 dhtcrawler.Config
	DhtCrawlerActive       *concurrency.AtomicValue[bool]                 `name:"dht_crawler_active"`
	DhtServerLastResponses *concurrency.AtomicValue[server.LastResponses] `name:"dht_server_last_responses"`
}

type Result struct {
	fx.Out
	Option health.CheckerOption `group:"health_check_options"`
}

func New(params Params) Result {
	return Result{
		Option: health.WithPeriodicCheck(
			time.Second*10,
			time.Second*1,
			NewCheck(params.Config, params.DhtCrawlerActive, params.DhtServerLastResponses),
		),
	}
}
