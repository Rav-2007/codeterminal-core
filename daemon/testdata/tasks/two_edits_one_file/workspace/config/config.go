package config

import "fmt"

// Config is the server configuration.
type Config struct {
	Host string
	Port int
}

// Default returns the configuration used when nothing is set.
func Default() Config {
	return Config{
		Host: "localhost",
		Port: 8080,
	}
}

// String renders the configuration for logs.
func (c Config) String() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
