package omdb

import (
	"context"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// requesterLazy defers instantiation until the first request, matching TMDB's pattern.
type requesterLazy struct {
	once      sync.Once
	config    Config
	logger    *zap.SugaredLogger
	err       error
	requester Requester
}

func (r *requesterLazy) Request(
	ctx context.Context,
	queryParams map[string]string,
	result any,
) (*resty.Response, error) {
	r.once.Do(func() {
		r.requester, r.err = newRequester(r.config, r.logger)
	})
	if r.err != nil {
		return nil, r.err
	}
	return r.requester.Request(ctx, queryParams, result)
}

func newRequester(config Config, logger *zap.SugaredLogger) (Requester, error) {
	if !config.Enabled {
		return nil, ErrDisabled
	}
	if config.APIKey == "" {
		return nil, ErrNoAPIKey
	}

	r := requesterLimiter{
		requester: requester{
			resty: resty.New().
				SetBaseURL(config.BaseURL).
				SetQueryParam("apikey", config.APIKey).
				SetRetryCount(2).
				SetRetryWaitTime(2 * time.Second).
				SetRetryMaxWaitTime(10 * time.Second).
				SetTimeout(10 * time.Second),
		},
		limiter: rate.NewLimiter(rate.Every(config.RateLimit), config.RateLimitBurst),
	}

	logger.Info("omdb: client initialized")
	return r, nil
}
