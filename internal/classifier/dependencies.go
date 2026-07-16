package classifier

import (
	"github.com/bitmagnet-io/bitmagnet/internal/omdb"
	"github.com/bitmagnet-io/bitmagnet/internal/tmdb"
	"github.com/bitmagnet-io/bitmagnet/internal/tpdb"
)

type dependencies struct {
	search     LocalSearch
	tmdbClient tmdb.Client
	omdbClient omdb.Client
	tpdbClient tpdb.Client
}
