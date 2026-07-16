package tpdb

import (
	"context"
	"errors"
)

// Client is the public interface for ThePornDB lookups.
type Client interface {
	SearchScenes(ctx context.Context, query string) (SearchResponse, error)
}

type client struct {
	requester Requester
}

// SearchScenes searches TPDB for scenes matching the given query string.
func (c client) SearchScenes(ctx context.Context, query string) (SearchResponse, error) {
	var response SearchResponse
	_, err := c.requester.Request(ctx, "/scenes", map[string]string{
		"parse": query,
	}, &response)
	if err != nil {
		return SearchResponse{}, err
	}
	return response, nil
}

var (
	ErrDisabled = errors.New("TPDB is disabled")
	ErrNoAPIKey = errors.New("TPDB API key not configured")
)
