package omdb

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
		BaseURL:        "https://www.omdbapi.com",
		RateLimit:      time.Second * 3, // ~1200/hr, well under free tier 1000/day
		RateLimitBurst: 1,
	}
}
