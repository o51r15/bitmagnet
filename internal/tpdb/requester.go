package tpdb

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"
)

// Requester handles HTTP requests to the TPDB API.
type Requester interface {
	Request(ctx context.Context, path string, queryParams map[string]string, result any) (*resty.Response, error)
}

type requester struct {
	resty *resty.Client
}

func (r requester) Request(
	ctx context.Context,
	path string,
	queryParams map[string]string,
	result any,
) (*resty.Response, error) {
	res, err := r.resty.R().
		SetContext(ctx).
		SetQueryParams(queryParams).
		SetResult(&result).
		Get(path)
	if err == nil && !res.IsSuccess() {
		switch res.StatusCode() {
		case http.StatusUnauthorized:
			err = ErrNoAPIKey
		case http.StatusNotFound:
			err = fmt.Errorf("TPDB: not found")
		default:
			err = fmt.Errorf("TPDB request failed: %s", res.Status())
		}
	}
	return res, err
}
