package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		content   string
		missing   bool
		required  bool
		override  string
		want      string
		wantError bool
	}{
		{name: "custom port", content: `{"listenPort":9090}`, want: ":9090"},
		{name: "CLI override", content: `{"listenPort":9090}`, override: "127.0.0.1:8181", want: "127.0.0.1:8181"},
		{name: "missing default file", missing: true, want: ":8080"},
		{name: "missing explicit file", missing: true, required: true, wantError: true},
		{name: "CLI without config", missing: true, override: ":9191", want: ":9191"},
		{name: "empty object", content: `{}`, want: ":8080"},
		{name: "zero port", content: `{"listenPort":0}`, wantError: true},
		{name: "negative port", content: `{"listenPort":-1}`, wantError: true},
		{name: "port too large", content: `{"listenPort":65536}`, wantError: true},
		{name: "wrong type", content: `{"listenPort":"9090"}`, wantError: true},
		{name: "typo", content: `{"listenPrt":9090}`, wantError: true},
		{name: "invalid JSON", content: `{"listenPort":`, wantError: true},
		{name: "trailing data", content: `{"listenPort":9090} {}`, wantError: true},
		{name: "null document", content: `null`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if !test.missing {
				if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadListenAddress(path, test.required, test.override)
			if (err != nil) != test.wantError {
				t.Fatalf("address=%q, error=%v; wantError=%t", got, err, test.wantError)
			}
			if err == nil && got != test.want {
				t.Fatalf("address=%q; want %q", got, test.want)
			}
		})
	}
}
