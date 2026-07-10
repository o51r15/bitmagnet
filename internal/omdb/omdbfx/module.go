package omdbfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/omdb"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"omdb",
		configfx.NewConfigModule[omdb.Config]("omdb", omdb.NewDefaultConfig()),
		fx.Provide(omdb.New),
	)
}
