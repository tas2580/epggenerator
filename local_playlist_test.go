package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalPlaylistLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local playlist.m3u8")
	if err := os.WriteFile(path, []byte(fixturePlaylist), 0600); err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDir, path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newApp(context.Background(), filepath.Join(dir, "data"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var playlistDownloads atomic.Int32
	a.client.Transport = handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tv.m3u8" {
			playlistDownloads.Add(1)
			io.WriteString(w, fixturePlaylist)
		} else {
			io.WriteString(w, fixtureEPG)
		}
	})}
	h := a.handler()
	settings := Settings{EPGURL: "http://source.test/epg.xml", PlaylistURL: "http://source.test/tv.m3u8", PlaylistFile: relative}
	if w := request(t, h, "PUT", "/api/settings", settings); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	if a.state.LastError != "" {
		t.Fatal(a.state.LastError)
	}
	if a.state.Settings.PlaylistFile != path {
		t.Fatalf("relative path not normalized: %q", a.state.Settings.PlaylistFile)
	}
	output := parseOutput(t, request(t, h, "GET", "/epg.xml", nil).Body.Bytes())
	if len(output.Channels) != 3 || len(output.Programmes) != 0 || playlistDownloads.Load() != 0 {
		t.Fatal("local import did not preserve all channels or used URL")
	}
	changes := map[string]any{"mappings": map[string]string{a.playlist.Channels[0].Key: "one"}}
	if w := request(t, h, "PATCH", "/api/mappings", changes); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	if source, err := os.ReadFile(path); err != nil || string(source) != fixturePlaylist {
		t.Fatal("original playlist was modified")
	}
	updated := fixturePlaylist + "#EXTINF:-1 tvg-id=\"new\",New channel\nhttp://tv/new\n"
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	before := request(t, h, "GET", "/epg.xml", nil).Body.Bytes()
	output = parseOutput(t, before)
	if len(output.Channels) != 4 || len(output.Programmes) != 1 {
		t.Fatal("local file was not reread or mapping was lost")
	}
	for _, failure := range []string{"invalid", "missing"} {
		if failure == "invalid" {
			if err := os.WriteFile(path, []byte("not a playlist"), 0600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := a.startRefresh(nil); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, a)
		if a.state.LastError == "" || !bytes.Equal(before, request(t, h, "GET", "/epg.xml", nil).Body.Bytes()) {
			t.Fatalf("%s local file replaced valid output or failed to report error", failure)
		}
	}
	restarted, err := newApp(context.Background(), a.dataDir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.state.Settings.PlaylistFile != path || restarted.state.Mappings["id:one"] != "one" {
		t.Fatal("local source/mapping did not survive restart")
	}
	if !bytes.Equal(before, request(t, restarted.handler(), "GET", "/epg.xml", nil).Body.Bytes()) {
		t.Fatal("restart lost cached output when original file was unavailable")
	}
	settings.PlaylistFile = ""
	if w := request(t, h, "PUT", "/api/settings", settings); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	output = parseOutput(t, request(t, h, "GET", "/epg.xml", nil).Body.Bytes())
	if a.state.LastError != "" || playlistDownloads.Load() != 1 || len(output.Channels) != 3 || len(output.Programmes) != 1 {
		t.Fatal("switching back to URL failed")
	}
}

func TestLocalPlaylistFileValidation(t *testing.T) {
	dir := t.TempDir()
	a, err := newApp(context.Background(), filepath.Join(dir, "data"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "too-large.m3u8")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(20*1024*1024 + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing.m3u8"), large} {
		if err := a.loadPlaylistSource(Settings{PlaylistFile: path}, filepath.Join(dir, "copy.m3u8")); err == nil {
			t.Fatalf("accepted invalid local source %q", path)
		}
	}
	if _, err := validateSettings(Settings{EPGURL: "https://epg.example/epg.xml", PlaylistFile: "./tv.m3u8"}); err != nil {
		t.Fatalf("local playlist must not require URL: %v", err)
	}
}
