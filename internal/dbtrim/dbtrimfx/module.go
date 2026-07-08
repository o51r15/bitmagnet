package dbtrimfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/dbtrim"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"dbtrim",
		configfx.NewConfigModule[dbtrim.Config]("db_trim", dbtrim.NewDefaultConfig()),
		fx.Provide(dbtrim.New),
	)
}
