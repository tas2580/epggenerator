package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// An absent default config preserves the standalone binary's previous behavior.
// A path explicitly supplied with -config must exist.
func loadListenAddress(path string, required bool, override string) (string, error) {
	config := &struct {
		ListenPort int `json:"listenPort"`
	}{ListenPort: 8080}
	f, err := os.Open(path)
	if err != nil {
		if required || !os.IsNotExist(err) {
			return "", fmt.Errorf("Konfiguration %q lesen: %w", path, err)
		}
	} else {
		defer f.Close()
		decoder := json.NewDecoder(f)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return "", fmt.Errorf("Konfiguration %q ist ungültig: %w", path, err)
		}
		if config == nil {
			return "", fmt.Errorf("Konfiguration %q muss ein JSON-Objekt sein", path)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return "", fmt.Errorf("Konfiguration %q darf nur ein JSON-Objekt enthalten", path)
		}
	}
	if config.ListenPort < 1 || config.ListenPort > 65535 {
		return "", fmt.Errorf("Konfiguration %q: listenPort muss zwischen 1 und 65535 liegen", path)
	}
	if override != "" {
		return override, nil
	}
	return fmt.Sprintf(":%d", config.ListenPort), nil
}
