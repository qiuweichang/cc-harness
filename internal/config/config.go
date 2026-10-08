// Package config loads local cc-hardness process configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config contains the host-level settings needed to start cc-hardness.
// DataRoot controls all persistent file access and WebRoot points to the
// compiled frontend assets served in production mode.
type Config struct {
	// Address is the local HTTP listen address.
	Address string
	// DataRoot is the absolute root containing catalog, config, and task directories.
	DataRoot string
	// WebRoot is the absolute directory containing the compiled React application.
	WebRoot string
}

// Load reads configuration from environment variables and resolves all paths.
// CC_HARDNESS_ADDR defaults to 127.0.0.1:8080, CC_HARDNESS_DATA_ROOT defaults
// to ./data, and CC_HARDNESS_WEB_ROOT defaults to ./web/dist.
func Load() (Config, error) {
	address := os.Getenv("CC_HARDNESS_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}

	dataRoot, err := resolvePath(os.Getenv("CC_HARDNESS_DATA_ROOT"), "data")
	if err != nil {
		return Config{}, fmt.Errorf("resolve data root: %w", err)
	}
	webRoot, err := resolvePath(os.Getenv("CC_HARDNESS_WEB_ROOT"), filepath.Join("web", "dist"))
	if err != nil {
		return Config{}, fmt.Errorf("resolve web root: %w", err)
	}

	return Config{Address: address, DataRoot: dataRoot, WebRoot: webRoot}, nil
}

// resolvePath returns an absolute, cleaned path using fallback when value is empty.
func resolvePath(value string, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
