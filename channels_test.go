package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func textPointer(value string) *string { return &value }

func newChannelTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tv.m3u8")
	if err := os.WriteFile(path, []byte(fixturePlaylist), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(context.Background(), filepath.Join(dir, "data"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a.client.Transport = handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, fixtureEPG) })}
	if err := a.startRefresh(&Settings{EPGURL: "http://source.test/epg.xml", PlaylistFile: path}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if a.state.LastError != "" {
		t.Fatal(a.state.LastError)
	}
	return a
}

func TestChannelEditsPersistAndExport(t *testing.T) {
	a := newChannelTestApp(t)
	h := a.handler()
	key := a.playlist.Channels[0].Key
	if err := a.updateMappings(map[string]string{key: "one"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	name, group := "Mein Sender, München & News", "Nachrichten & Regional"
	changes := map[string]any{"channels": map[string]*ChannelEdit{key: {Name: &name, Group: &group}}}
	if w := request(t, h, "PATCH", "/api/channels", changes); w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	if a.state.LastError != "" {
		t.Fatal(a.state.LastError)
	}
	output := parseOutput(t, request(t, h, "GET", "/epg.xml", nil).Body.Bytes())
	if len(output.Channels) != 3 || output.Channels[0].Name != name || output.Channels[0].ID != "one" || len(output.Programmes) != 1 {
		t.Fatal("edit changed identity, lost programmes/channels or failed to rename XMLTV channel")
	}
	pl, err := parsePlaylist(request(t, h, "GET", "/playlist.m3u8", nil).Body)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Channels[0].Name != name || pl.Channels[0].Group != group || pl.Channels[0].URL != a.playlist.Channels[0].URL || pl.Channels[0].ID != "one" {
		t.Fatal("playlist edit not exported correctly")
	}
	if !strings.Contains(pl.Lines[pl.Channels[0].Line], `tvg-name="`+name+`"`) {
		t.Fatal("tvg-name not updated")
	}
	var view struct {
		Channels []ChannelView `json:"channels"`
	}
	if err := json.Unmarshal(request(t, h, "GET", "/api/state", nil).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Channels[0].Name != name || view.Channels[0].Group != group || !view.Channels[0].Edited || view.Channels[0].OriginalName != "One & News HD" {
		t.Fatal("view must show edited values and original values")
	}
	if a.playlist.Channels[0].Name != "One & News HD" {
		t.Fatal("edit mutated the source playlist")
	}
	original, err := os.ReadFile(a.state.Settings.PlaylistFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != fixturePlaylist {
		t.Fatal("original file modified")
	}
	updated := strings.Replace(fixturePlaylist, "One & News HD", "Updated original", 1)
	if err := os.WriteFile(a.state.Settings.PlaylistFile, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	output = parseOutput(t, request(t, h, "GET", "/epg.xml", nil).Body.Bytes())
	if output.Channels[0].Name != name {
		t.Fatal("refresh overwrote custom name")
	}
	restarted, err := newApp(context.Background(), a.dataDir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if *restarted.state.ChannelEdits[key].Name != name || *restarted.state.ChannelEdits[key].Group != group || restarted.state.Mappings[key] != "one" {
		t.Fatal("edits/mapping did not survive restart")
	}
	// Empty category is an intentional override, not a reset.
	if err := a.updateChannels(map[string]*ChannelEdit{key: {Group: textPointer("")}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	pl, err = parsePlaylist(request(t, h, "GET", "/playlist.m3u8", nil).Body)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Channels[0].Group != "" || pl.Channels[0].Name != name {
		t.Fatal("clearing category lost name override or retained category")
	}
	if w := request(t, h, "PATCH", "/api/channels", map[string]any{"channels": map[string]any{key: nil}}); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	pl, err = parsePlaylist(request(t, h, "GET", "/playlist.m3u8", nil).Body)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Channels[0].Name != "Updated original" || pl.Channels[0].Group != "News, TV" || len(a.state.ChannelEdits) != 0 {
		t.Fatal("reset did not restore current source values")
	}
}

func TestChannelEditsValidationAndIndependentFields(t *testing.T) {
	a := newChannelTestApp(t)
	key := a.playlist.Channels[0].Key
	before := request(t, a.handler(), "GET", "/playlist.m3u8", nil).Body.Bytes()
	for _, changes := range []map[string]*ChannelEdit{
		{key: {Name: textPointer(" ")}},
		{key: {Name: textPointer("Bad\n#EXTINF:0,Injected")}},
		{key: {Group: textPointer("Bad\rcategory")}},
		{key: {Group: textPointer("Bad\u0085category")}},
		{key: {Group: textPointer(`Bad "quote"`)}},
		{"missing": {Name: textPointer("New name")}},
	} {
		if err := a.updateChannels(changes); err == nil {
			t.Fatal("invalid edit accepted")
		}
		if len(a.state.ChannelEdits) != 0 || !bytes.Equal(before, request(t, a.handler(), "GET", "/playlist.m3u8", nil).Body.Bytes()) {
			t.Fatal("invalid edit changed output")
		}
	}
	if err := a.updateChannels(map[string]*ChannelEdit{key: {Name: textPointer("Custom name"), Group: textPointer(a.playlist.Channels[0].Group)}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if a.state.ChannelEdits[key].Group != nil {
		t.Fatal("unchanged category should follow source updates")
	}
	updated := strings.Replace(fixturePlaylist, `group-title="News, TV"`, `group-title="New category"`, 1)
	if err := os.WriteFile(a.state.Settings.PlaylistFile, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	p, err := parsePlaylist(request(t, a.handler(), "GET", "/playlist.m3u8", nil).Body)
	if err != nil {
		t.Fatal(err)
	}
	if p.Channels[0].Group != "New category" || p.Channels[0].Name != "Custom name" {
		t.Fatal("unedited field must follow source updates")
	}
}

func TestLogoPreview(t *testing.T) {
	a := newChannelTestApp(t)
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aM/wAAAAASUVORK5CYII=")
	a.playlist.Channels[0].Logo = "http://tv.lan/logos/ARD_HD.png"
	calls := 0
	mode := "image"
	a.client.Transport = handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.String() != "http://tv.lan/logos/ARD_HD.png" {
			t.Errorf("unexpected logo URL: %s", r.URL)
		}
		switch mode {
		case "image":
			w.Header().Set("Content-Type", "image/png")
			w.Write(image)
		case "missing":
			http.NotFound(w, r)
		case "html":
			io.WriteString(w, "<html>not an image</html>")
		case "large":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, strings.Repeat("x", 5*1024*1024+1))
		}
	})}
	h := a.handler()
	w := request(t, h, "GET", "/api/logo?key=id%3Aone", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), image) || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Cache-Control"), "max-age=300") {
		t.Fatal("logo preview not served correctly")
	}
	if w := request(t, h, "HEAD", "/api/logo?key=id%3Aone", nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD logo response incorrect")
	}
	before := calls
	if w := request(t, h, "GET", "/api/logo?key=unknown&url=http://other.test/", nil); w.Code != 404 || calls != before {
		t.Fatal("unknown channel caused a request")
	}
	for _, failure := range []string{"missing", "html", "large"} {
		mode = failure
		if w := request(t, h, "GET", "/api/logo?key=id%3Aone", nil); w.Code != http.StatusBadGateway || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s logo failure was not handled", failure)
		}
	}
	mode = "image"
	a.playlist.Channels[0].Logo = ""
	channel := a.epg.ByID["one"]
	channel.Icon.Src = "http://tv.lan/logos/ARD_HD.png"
	a.epg.ByID["one"] = channel
	a.state.Mappings["id:one"] = "one"
	if w := request(t, h, "GET", "/api/logo?key=id%3Aone", nil); w.Code != 200 {
		t.Fatal("missing playlist logo must fall back to assigned EPG logo")
	}
}
