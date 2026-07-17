package rssfeedhttp

import (
	"net/http"

	"github.com/bitmagnet-io/bitmagnet/internal/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/rssfeed"
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	PollNowFn rssfeed.PollNowFunc `name:"rssfeed_poll_now"`
}

type Result struct {
	fx.Out
	Option httpserver.Option `group:"http_server_options"`
}

func New(p Params) Result {
	return Result{
		Option: &builder{pollNow: p.PollNowFn},
	}
}

type builder struct {
	pollNow rssfeed.PollNowFunc
}

func (builder) Key() string { return "rssfeed_api" }

type pollRequest struct {
	FeedNames []string `json:"feedNames"`
}

func (b builder) Apply(e *gin.Engine) error {
	// POST /api/rss/poll — triggers an on-demand poll for the given feed names.
	// Returns 202 Accepted immediately; the poll runs asynchronously.
	e.POST("/api/rss/poll", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
		var req pollRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if len(req.FeedNames) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feedNames must not be empty"})
			return
		}
		const maxFeeds = 100
		if len(req.FeedNames) > maxFeeds {
			c.JSON(http.StatusBadRequest, gin.H{"error": "too many feedNames"})
			return
		}
		for _, name := range req.FeedNames {
			if name == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "feedNames must not contain empty strings"})
				return
			}
		}
		for _, name := range req.FeedNames {
			b.pollNow(name)
		}
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "count": len(req.FeedNames)})
	})
	return nil
}
