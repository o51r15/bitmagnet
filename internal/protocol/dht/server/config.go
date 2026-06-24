package server

import "time"

type Config struct {
	Port         uint16
	QueryTimeout time.Duration
	// MaxConcurrentQueries caps the number of in-flight UDP queries across the
	// whole server. Without this, at scaling_factor 10 up to 1,010 goroutines
	// can be simultaneously blocked on UDP send/receive. The semaphore adds
	// backpressure without touching channel sizing or pipeline architecture.
	// At scaling_factor 1 (~101 max goroutines) a cap of 512 never engages;
	// it becomes meaningful when scaling_factor is raised above 4.
	MaxConcurrentQueries int64
}

func NewDefaultConfig() Config {
	return Config{
		Port:                 3334,
		QueryTimeout:         time.Second * 4,
		MaxConcurrentQueries: 512,
	}
}
