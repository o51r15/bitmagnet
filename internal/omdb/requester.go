package omdb

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

// Requester is the OMDb equivalent of TMDB's Requester interface.
type Requester interface {
	Request(ctx context.Context, queryParams map[string]string, result any) (*resty.Response, error)
}

type requester struct {
	resty *resty.Client
}

func (r requester) Request(
	ctx context.Context,
	queryParams map[string]string,
	result any,
) (*resty.Response, error) {
	res, err := r.resty.R().
		SetContext(ctx).
		SetQueryParams(queryParams).
		SetResult(&result).
		Get("/")
	if err == nil && !res.IsSuccess() {
		switch res.StatusCode() {
		case http.StatusUnauthorized:
			err = ErrNoAPIKey
		default:
			err = fmt.Errorf("OMDb request failed: %s", res.Status())
		}
	}
	return res, err
}
