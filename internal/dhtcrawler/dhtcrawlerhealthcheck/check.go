package dhtcrawlerhealthcheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/dhtcrawler"
	"github.com/bitmagnet-io/bitmagnet/internal/health"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/server"
)

func NewCheck(
	config dhtcrawler.Config,
	dhtCrawlerActive *concurrency.AtomicValue[bool],
	lastResponses *concurrency.AtomicValue[server.LastResponses],
) health.Check {
	sidecarClient := &http.Client{Timeout: 3 * time.Second}

	return health.Check{
		Name: "dht",
		IsActive: func() bool {
			// Active if the local crawler is running OR a sidecar is configured
			return dhtCrawlerActive.Get() || config.SidecarEnabled
		},
		Timeout: 5 * time.Second,
		Check: func(ctx context.Context) error {
			// If the local DHT crawler is running, use the original local check
			if dhtCrawlerActive.Get() {
				lr := lastResponses.Get()
				if lr.StartTime.IsZero() {
					return nil
				}
				now := time.Now()
				if lr.LastSuccess.IsZero() {
					if now.Sub(lr.StartTime) < 30*time.Second {
						return nil
					}
					return errors.New("no response within 30 seconds")
				}
				if now.Sub(lr.LastSuccess) > time.Minute {
					return errors.New("no successful responses within last minute")
				}
				return nil
			}

			// Sidecar mode: probe the sidecar HTTP server
			if config.SidecarEnabled {
				if config.SidecarURL == "" {
					return errors.New("dht sidecar enabled but no URL configured")
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.SidecarURL, nil)
				if err != nil {
					return fmt.Errorf("dht sidecar: invalid URL: %w", err)
				}
				resp, err := sidecarClient.Do(req)
				if err != nil {
					return fmt.Errorf("dht sidecar unreachable: %w", err)
				}
				resp.Body.Close()
				if resp.StatusCode >= 500 {
					return fmt.Errorf("dht sidecar unhealthy: HTTP %d", resp.StatusCode)
				}
				return nil
			}

			return errors.New("dht crawler not active")
		},
	}
}
