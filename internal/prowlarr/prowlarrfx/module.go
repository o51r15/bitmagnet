package prowlarrfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/prowlarr"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"prowlarr",
		configfx.NewConfigModule[prowlarr.Config]("prowlarr", prowlarr.NewDefaultConfig()),
		fx.Provide(prowlarr.New),
	)
}
