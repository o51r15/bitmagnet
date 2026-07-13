package dbimportfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/dbimport"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"dbimport",
		configfx.NewConfigModule[dbimport.Config]("db_import", dbimport.NewDefaultConfig()),
		fx.Provide(dbimport.NewHTTPServer),
	)
}
