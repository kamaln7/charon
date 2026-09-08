package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Use the same XDG location on every platform, including macOS.
func serverConfig() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	configPath := filepath.Join(dir, "charonctl", "config.json")
	cfg := &struct {
		API string `json:"api"`
	}{}
	f, err := os.Open(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		defer f.Close()
		dec := json.NewDecoder(f)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return "", fmt.Errorf("config %s: %w", configPath, err)
		}
		if cfg == nil {
			return "", fmt.Errorf("config %s: expected a JSON object", configPath)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return "", fmt.Errorf("config %s: expected a single JSON object", configPath)
		}
	}
	if base := os.Getenv("CHARON_API"); base != "" {
		return base, nil
	}
	return cfg.API, nil
}
