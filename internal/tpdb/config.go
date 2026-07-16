package tpdb

import "time"

type Config struct {
	Enabled        bool
	APIKey         string
	BaseURL        string
	RateLimit      time.Duration
	RateLimitBurst int
}

func NewDefaultConfig() Config {
	return Config{
		Enabled:        false,
		BaseURL:        "https://api.theporndb.net",
		RateLimit:      time.Second, // 1 req/sec conservative default (API allows 2/sec)
		RateLimitBurst: 1,
	}
}
