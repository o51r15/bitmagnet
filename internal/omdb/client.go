package omdb

import (
	"context"
	"errors"
	"fmt"
)

// Client is the public interface for OMDb lookups.
type Client interface {
	LookupByIMDBID(ctx context.Context, imdbID string) (LookupResult, error)
}

type client struct {
	requester Requester
}

// LookupByIMDBID fetches OMDb data for a given IMDB ID (e.g. "tt1234567").
func (c client) LookupByIMDBID(ctx context.Context, imdbID string) (LookupResult, error) {
	var response LookupResult
	_, err := c.requester.Request(ctx, map[string]string{
		"i":    imdbID,
		"plot": "short",
	}, &response)
	if err != nil {
		return LookupResult{}, err
	}
	if response.Response == "False" {
		return LookupResult{}, fmt.Errorf("omdb: %s", response.Error)
	}
	return response, nil
}

var (
	ErrDisabled = errors.New("OMDb is disabled")
	ErrNoAPIKey = errors.New("OMDb API key not configured")
)
