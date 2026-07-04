package prowlarrhttp

import (
	"net/http"

	"github.com/bitmagnet-io/bitmagnet/internal/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/prowlarr"
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	CrawlNowFn prowlarr.CrawlNowFunc `name:"prowlarr_crawl_now"`
}

type Result struct {
	fx.Out
	Option httpserver.Option `group:"http_server_options"`
}

func New(p Params) Result {
	return Result{
		Option: &builder{crawlNow: p.CrawlNowFn},
	}
}

type builder struct {
	crawlNow prowlarr.CrawlNowFunc
}

func (builder) Key() string { return "prowlarr_api" }

type crawlRequest struct {
	IndexerIDs []int `json:"indexerIds"`
}

func (b builder) Apply(e *gin.Engine) error {
	// POST /api/prowlarr/crawl — triggers an on-demand crawl for the given indexer IDs.
	// Returns 202 Accepted immediately; the crawl runs asynchronously.
	// If the prowlarr crawler is not configured (no API key) the call is a no-op.
	e.POST("/api/prowlarr/crawl", func(c *gin.Context) {
		var req crawlRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if len(req.IndexerIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "indexerIds must not be empty"})
			return
		}
		for _, id := range req.IndexerIDs {
			b.crawlNow(id)
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "count": len(req.IndexerIDs)})
	})
	return nil
}
