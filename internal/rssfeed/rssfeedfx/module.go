package rssfeedfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/rssfeed"
	rssfeedhttp "github.com/bitmagnet-io/bitmagnet/internal/rssfeed/httpserver"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"rssfeed",
		configfx.NewConfigModule[rssfeed.Config]("rss", rssfeed.NewDefaultConfig()),
		fx.Provide(rssfeed.New),
		fx.Provide(rssfeedhttp.New),
	)
}
