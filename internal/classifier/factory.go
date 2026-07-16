package classifier

import (
	"fmt"

	"github.com/bitmagnet-io/bitmagnet/internal/database/search"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/omdb"
	"github.com/bitmagnet-io/bitmagnet/internal/tmdb"
	"github.com/bitmagnet-io/bitmagnet/internal/tpdb"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	Config     Config
	TmdbConfig tmdb.Config
	OmdbConfig omdb.Config
	TpdbConfig tpdb.Config
	Search     lazy.Lazy[search.Search]
	TmdbClient lazy.Lazy[tmdb.Client]
	OmdbClient lazy.Lazy[omdb.Client]
	TpdbClient lazy.Lazy[tpdb.Client]
}

type Result struct {
	fx.Out
	Compiler lazy.Lazy[Compiler]
	Source   lazy.Lazy[Source]
	Runner   lazy.Lazy[Runner]
}

func New(params Params) Result {
	lc := lazy.New(func() (Compiler, error) {
		s, err := params.Search.Get()
		if err != nil {
			return nil, err
		}

		tmdbClient, err := params.TmdbClient.Get()
		if err != nil {
			return nil, err
		}

		// OMDb is optional - if disabled or no API key, first use returns nil gracefully.
		omdbClient, _ := params.OmdbClient.Get()

		// TPDB is optional - if disabled or no API key, first use returns nil gracefully.
		tpdbClient, _ := params.TpdbClient.Get()

		return compiler{
			options: []compilerOption{
				compilerFeatures(defaultFeatures),
				celEnvOption,
			},
			dependencies: dependencies{
				search: localSearchSemaphore{
					search:    localSearch{s},
					semaphore: make(chan struct{}, 1),
				},
				tmdbClient: tmdbClient,
				omdbClient: omdbClient,
				tpdbClient: tpdbClient,
			},
		}, nil
	})
	lsrc := lazy.New[Source](func() (Source, error) {
		src, err := newSourceProvider(params.Config, params.TmdbConfig, params.OmdbConfig, params.TpdbConfig).source()
		if err != nil {
			return Source{}, err
		}

		if _, ok := src.Workflows[params.Config.Workflow]; !ok {
			return Source{}, fmt.Errorf("default workflow '%s' not found", params.Config.Workflow)
		}

		return src, nil
	})

	return Result{
		Compiler: lc,
		Source:   lsrc,
		Runner: lazy.New(func() (Runner, error) {
			src, err := lsrc.Get()
			if err != nil {
				return nil, err
			}
			c, err := lc.Get()
			if err != nil {
				return nil, err
			}
			r, err := c.Compile(src)
			if err != nil {
				return nil, err
			}

			return runnerSemaphore{
				runner:    r,
				semaphore: make(chan struct{}, params.Config.Concurrency),
			}, nil
		}),
	}
}
