package tpdbfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/tpdb"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"tpdb",
		configfx.NewConfigModule[tpdb.Config]("tpdb", tpdb.NewDefaultConfig()),
		fx.Provide(tpdb.New),
	)
}
