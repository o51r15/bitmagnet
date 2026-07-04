package seedlookupfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/config/configfx"
	"github.com/bitmagnet-io/bitmagnet/internal/seedlookup"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"seed_lookup",
		configfx.NewConfigModule[seedlookup.Config]("seed_lookup", seedlookup.NewDefaultConfig()),
		fx.Provide(seedlookup.New),
	)
}
