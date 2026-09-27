package config

import (
	"fmt"
	"time"
)

// Config is the server configuration.
type Config struct {
	Host    string
	Port    int
	Timeout time.Duration
}

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Host:    "localhost",
		Port:    8080,
		Timeout: 30 * time.Second,
	}
}

// String renders the configuration for logs.
func (c Config) String() string {
	return fmt.Sprintf("%s:%d (timeout %s)", c.Host, c.Port, c.Timeout)
}
