package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixturePlaylist = "#EXTM3U url-tvg=\"http://old/epg.xml\"\n#EXTINF:-1 tvg-id=\"one\" group-title=\"News, TV\",One & News HD\n#EXTVLCOPT:network-caching=1000\nrtsp://tv/one\n#EXTINF:-1 tvg-name=\"Two\",Two HD\nhttp://tv/two\n#EXTINF:-1 tvg-id=\"three\",No EPG\nhttp://tv/three\n"
const fixtureEPG = `<?xml version="1.0"?><tv><channel id="one"><display-name>DE - One &amp; News</display-name></channel><channel id="two"><display-name>DE - Two</display-name></channel><channel id="outside"><display-name>Outside</display-name></channel><programme start="20261019080000 +0200" stop="20261019090000 +0200" channel="one"><title lang="de">News &amp; more</title><desc>Details</desc><category lang="de">News</category><episode-num system="onscreen">S1E2</episode-num><rating system="FSK"><value>6</value></rating></programme><programme start="20261019090000 +0200" channel="two"><title>Second</title></programme><programme start="20261019090000 +0200" channel="outside"><title>Excluded</title></programme></tv>`

type parsedOutput struct {
	Channels []struct {
		ID   string `xml:"id,attr"`
		Name string `xml:"display-name"`
	} `xml:"channel"`
	Programmes []struct {
		Channel     string `xml:"channel,attr"`
		Start       string `xml:"start,attr"`
		Title       string `xml:"title"`
		Description string `xml:"desc"`
		Episode     string `xml:"episode-num"`
		Rating      string `xml:"rating>value"`
	} `xml:"programme"`
}

func parseOutput(t *testing.T, b []byte) parsedOutput {
	t.Helper()
	var output parsedOutput
	if err := xml.Unmarshal(b, &output); err != nil {
		preview := b
		if len(preview) > 1000 {
			preview = preview[:1000]
		}
		t.Fatalf("invalid output: %v\n%s", err, preview)
	}
	ids := map[string]bool{}
	for _, c := range output.Channels {
		if c.ID == "" || ids[c.ID] {
			t.Fatalf("missing/duplicate output ID %q", c.ID)
		}
		ids[c.ID] = true
	}
	for _, p := range output.Programmes {
		if !ids[p.Channel] {
			t.Fatalf("dangling programme channel %q", p.Channel)
		}
	}
	return output
}

func TestPlaylistIDsAndMetadata(t *testing.T) {
	p, err := parsePlaylist(strings.NewReader("\ufeff" + strings.ReplaceAll(fixturePlaylist, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Channels) != 3 || p.Channels[0].Group != "News, TV" || p.Channels[0].ID != "one" {
		t.Fatalf("unexpected playlist: %+v", p.Channels)
	}
	if !strings.HasPrefix(p.Channels[1].ID, "epggenerator.") {
		t.Fatal("missing stable ID")
	}
	var b bytes.Buffer
	if err := p.Write(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "#EXTVLCOPT:network-caching=1000") {
		t.Fatal("lost VLC metadata")
	}
	copy, err := parsePlaylist(&b)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range copy.Channels {
		if c.ID != p.Channels[i].ID || c.URL != p.Channels[i].URL || c.Name != p.Channels[i].Name {
			t.Fatal("playlist round-trip changed channel/stream")
		}
	}
	reordered := "#EXTM3U\n#EXTINF:-1,Two HD\nhttp://tv/two\n#EXTINF:-1 tvg-id=\"one\",One\nrtsp://tv/one\n"
	p2, err := parsePlaylist(strings.NewReader(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if p2.Channels[0].ID != p.Channels[1].ID {
		t.Fatal("fallback ID changed after reorder")
	}
	duplicate := "#EXTM3U\n#EXTINF:-1 tvg-id=\"same\",A\nhttp://tv/a\n#EXTINF:-1 tvg-id=\"same\",B\nhttp://tv/b\n#EXTINF:-1,C\nhttp://tv/c\n#EXTINF:-1,D\nhttp://tv/c\n"
	p3, err := parsePlaylist(strings.NewReader(duplicate))
	if err != nil {
		t.Fatal(err)
	}
	if p3.Channels[0].ID == p3.Channels[1].ID || p3.Channels[0].Ambiguous || !p3.Channels[2].Ambiguous || !p3.Channels[3].Ambiguous || p3.Channels[2].ID == p3.Channels[3].ID {
		t.Fatalf("duplicate handling: %+v", p3.Channels)
	}
}

func TestXMLTVKeepsAllPlaylistChannels(t *testing.T) {
	p, err := parsePlaylist(strings.NewReader(fixturePlaylist))
	if err != nil {
		t.Fatal(err)
	}
	index, err := readEPG(strings.NewReader(fixtureEPG))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		mappings   map[string]string
		programmes int
	}{
		{"none", map[string]string{}, 0},
		{"one", map[string]string{p.Channels[0].Key: "one"}, 1},
		{"shared", map[string]string{p.Channels[0].Key: "one", p.Channels[1].Key: "one"}, 2},
		{"missing", map[string]string{p.Channels[0].Key: "gone"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			n, err := writeEPG(&b, strings.NewReader(fixtureEPG), p, index, test.mappings)
			if err != nil {
				t.Fatal(err)
			}
			output := parseOutput(t, b.Bytes())
			if len(output.Channels) != 3 || len(output.Programmes) != test.programmes || n != test.programmes {
				t.Fatalf("got %d channels/%d programmes", len(output.Channels), len(output.Programmes))
			}
			for i, c := range output.Channels {
				if c.ID != p.Channels[i].ID || c.Name != p.Channels[i].Name {
					t.Fatal("playlist identity lost")
				}
			}
			for _, programme := range output.Programmes {
				if programme.Title != "News & more" || programme.Description != "Details" || programme.Episode != "S1E2" || programme.Rating != "6" || programme.Start != "20261019080000 +0200" {
					t.Fatalf("metadata lost: %+v", programme)
				}
			}
		})
	}
}

func TestInputValidationAndSuggestions(t *testing.T) {
	for _, input := range []string{"", "<html></html>", "<tv>", "<tv></tv>", "<tv><channel id=\"a\"/></tv><tv/>", "<tv><channel id=\"a\"/><channel id=\"a\"/></tv>"} {
		if _, err := readEPG(strings.NewReader(input)); err == nil {
			t.Errorf("accepted invalid EPG %q", input)
		}
	}
	unnamed, err := readEPG(strings.NewReader(`<tv><channel id="unnamed"/></tv>`))
	if err != nil || len(unnamed.Channels[0].Names) != 1 || unnamed.Channels[0].Names[0] != "unnamed" {
		t.Fatal("channel ID must be available as fallback display name")
	}
	for _, input := range []string{"", "<html>Error</html>", "#EXTM3U\n#EXTINF:-1,Test", "#EXTM3U\nhttp://tv/a"} {
		if _, err := parsePlaylist(strings.NewReader(input)); err == nil {
			t.Errorf("accepted invalid playlist %q", input)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.org/a", "http://", "/epg.xml"} {
		if _, err := validateSettings(Settings{EPGURL: raw, PlaylistURL: "http://tv.lan/tv.m3u8"}); err == nil {
			t.Errorf("accepted invalid URL %q", raw)
		}
	}
	p, _ := parsePlaylist(strings.NewReader(fixturePlaylist))
	index, _ := readEPG(strings.NewReader(fixtureEPG))
	if got := suggestions(p.Channels[0], index); len(got) == 0 || got[0].ID != "one" || got[0].Reason != "Gleiche Sender-ID" {
		t.Fatalf("missing ID suggestion: %+v", got)
	}
	if got := suggestions(p.Channels[1], index); len(got) == 0 || got[0].ID != "two" {
		t.Fatalf("missing normalized name suggestion: %+v", got)
	}
}

func waitIdle(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.RLock()
		busy := a.busy
		a.mu.RUnlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("background operation did not complete")
}

func request(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, payload)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Exercise the actual download/handler path without requiring a listening socket.
type handlerTransport struct{ handler http.Handler }

func (transport handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	w := httptest.NewRecorder()
	transport.handler.ServeHTTP(w, r)
	response := w.Result()
	response.Request = r
	return response, nil
}

func TestServiceLifecycleAndRecovery(t *testing.T) {
	var mode atomic.Int32
	source := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 1 {
			http.Error(w, "Unavailable", 503)
			return
		}
		if r.URL.Path == "/tv.m3u8" {
			if mode.Load() == 3 {
				io.WriteString(w, "#EXTM3U\n#EXTINF:-1 tvg-id=\"one\",One\nrtsp://tv/one\n")
				return
			}
			io.WriteString(w, fixturePlaylist)
		} else {
			if mode.Load() == 4 {
				io.WriteString(w, `<tv><channel id="broken">`)
				return
			}
			if mode.Load() == 2 {
				io.WriteString(w, strings.ReplaceAll(fixtureEPG, `id="one"`, `id="changed"`))
				return
			}
			io.WriteString(w, fixtureEPG)
		}
	})
	dir := t.TempDir()
	a, err := newApp(context.Background(), dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a.client.Transport = handlerTransport{handler: source}
	h := a.handler()
	if w := request(t, h, "GET", "/epg.xml", nil); w.Code != 503 {
		t.Fatal("expected no output before first import")
	}
	settings := Settings{EPGURL: "http://source.test/epg.xml", PlaylistURL: "http://source.test/tv.m3u8"}
	if w := request(t, h, "PUT", "/api/settings", settings); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	w := request(t, h, "GET", "/epg.xml", nil)
	output := parseOutput(t, w.Body.Bytes())
	if w.Code != 200 || len(output.Channels) != 3 || len(output.Programmes) != 0 {
		t.Fatal("initial export must contain all channels and no programmes")
	}
	changes := map[string]any{"mappings": map[string]string{a.playlist.Channels[0].Key: "one", a.playlist.Channels[1].Key: "one"}}
	if w := request(t, h, "PATCH", "/api/mappings", changes); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	w = request(t, h, "GET", "/epg.xml?download=1", nil)
	if !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download disposition missing")
	}
	before := append([]byte(nil), w.Body.Bytes()...)
	if len(parseOutput(t, before).Programmes) != 2 {
		t.Fatal("mapping export missing programmes")
	}
	pl := request(t, h, "GET", "/playlist.m3u8", nil)
	if !strings.Contains(pl.Body.String(), `url-tvg="http://example.com/epg.xml"`) || strings.Contains(pl.Body.String(), "http://old/") {
		t.Fatal("playlist EPG URL not replaced")
	}
	p, err := parsePlaylist(pl.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Channels) != 3 || p.Channels[1].ID != a.playlist.Channels[1].ID {
		t.Fatal("generated playlist differs from output IDs")
	}
	restarted, err := newApp(context.Background(), dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.state.Settings != settings || restarted.state.Mappings[a.playlist.Channels[1].Key] != "one" {
		t.Fatal("settings/mappings did not survive restart")
	}
	if got := request(t, restarted.handler(), "GET", "/epg.xml", nil).Body.Bytes(); !bytes.Equal(got, before) {
		t.Fatal("restart changed output")
	}
	mode.Store(1)
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if a.state.LastError == "" {
		t.Fatal("missing download failure status")
	}
	if got := request(t, h, "GET", "/epg.xml", nil).Body.Bytes(); !bytes.Equal(got, before) {
		t.Fatal("failed refresh replaced valid output")
	}
	mode.Store(4)
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if a.state.LastError == "" || !bytes.Equal(request(t, h, "GET", "/epg.xml", nil).Body.Bytes(), before) {
		t.Fatal("malformed XML replaced valid output or did not report an error")
	}
	mode.Store(2)
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	w = request(t, h, "GET", "/epg.xml", nil)
	output = parseOutput(t, w.Body.Bytes())
	if len(output.Channels) != 3 || len(output.Programmes) != 0 {
		t.Fatal("missing EPG sender must retain playlist channels without programmes")
	}
	status := request(t, h, "GET", "/api/state", nil)
	if !strings.Contains(status.Body.String(), `"status":"missing"`) {
		t.Fatal("missing source not flagged")
	}
	mode.Store(3)
	if err := a.startRefresh(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	w = request(t, h, "GET", "/epg.xml", nil)
	output = parseOutput(t, w.Body.Bytes())
	if len(output.Channels) != 1 || len(output.Programmes) != 1 {
		t.Fatal("removed playlist channels leaked into output")
	}
	if a.state.LastError != "" {
		t.Fatal("successful update did not clear error")
	}
	// Settings changes persist even when their source fails; old output stays intact.
	mode.Store(1)
	settings.EPGURL = "http://source.test/new-epg.xml"
	if w := request(t, h, "PUT", "/api/settings", settings); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	loaded, err := newApp(context.Background(), dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.state.Settings.EPGURL != settings.EPGURL || loaded.state.LastError == "" {
		t.Fatal("new URL/failure status not persisted")
	}
	clear := map[string]any{"mappings": map[string]string{a.playlist.Channels[0].Key: ""}}
	if w := request(t, h, "PATCH", "/api/mappings", clear); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitIdle(t, a)
	output = parseOutput(t, request(t, h, "GET", "/epg.xml", nil).Body.Bytes())
	if len(output.Channels) != 1 || len(output.Programmes) != 0 {
		t.Fatal("clearing a mapping must retain its channel without programmes")
	}
}

func TestConcurrentUpdatesAndHTTPGuards(t *testing.T) {
	a, err := newApp(context.Background(), t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.begin(); err != nil {
		t.Fatal(err)
	}
	if err := a.startRefresh(nil); err == nil {
		t.Fatal("accepted overlapping update")
	}
	a.finish(nil)
	h := a.handler()
	r := httptest.NewRequest("POST", "/api/refresh", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("accepted cross-origin mutation")
	}
	for _, path := range []string{"/", "/app.js", "/style.css", "/channels.css", "/theme.js", "/theme.css"} {
		if w := request(t, h, "GET", path, nil); w.Code != 200 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
	if w := request(t, h, "GET", "/index.html", nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "./" {
		t.Fatalf("index.html should redirect to the start page: status=%d, location=%q", w.Code, w.Header().Get("Location"))
	}
	if w := request(t, h, "GET", "/api/state", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"channels"`) {
		t.Fatalf("state route unavailable: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, "HEAD", "/", nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD should return headers without a body: %d", w.Code)
	}
	if w := request(t, h, "GET", "/api/refresh", nil); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatal("GET must not trigger a refresh")
	}
	if w := request(t, h, "POST", "/api/state", map[string]string{}); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("incorrect method handling for state route")
	}
	if w := request(t, h, "GET", "/state.json", nil); w.Code != 404 {
		t.Fatal("private file route exposed")
	}
}

func TestProvidedFiles(t *testing.T) {
	playlistFile, err := os.Open("tv.m3u8")
	if os.IsNotExist(err) {
		t.Skip("local sample files unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer playlistFile.Close()
	epgPath := "epg-de.xml"
	if _, err := os.Stat(epgPath); os.IsNotExist(err) {
		t.Skip("local EPG sample unavailable")
	}
	p, err := parsePlaylist(playlistFile)
	if err != nil {
		t.Fatal(err)
	}
	epgFile, err := os.Open(epgPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := readEPG(epgFile)
	epgFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Channels) != 54 || len(index.Channels) != 438 || index.Programmes != 78820 {
		t.Fatalf("unexpected sample counts: %d / %d / %d", len(p.Channels), len(index.Channels), index.Programmes)
	}
	mappings := map[string]string{}
	missing := 0
	for _, c := range p.Channels {
		if c.SourceID == "" {
			missing++
		}
		if _, ok := index.ByID[c.SourceID]; ok {
			mappings[c.Key] = c.SourceID
		}
	}
	if missing != 15 || len(mappings) != 38 {
		t.Fatalf("unexpected ID counts: %d missing / %d matches", missing, len(mappings))
	}
	for _, withMappings := range []bool{false, true} {
		target := filepath.Join(t.TempDir(), "epg.xml")
		out, err := os.Create(target)
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.Open(epgPath)
		if err != nil {
			t.Fatal(err)
		}
		selected := map[string]string{}
		if withMappings {
			selected = mappings
		}
		count, err := writeEPG(out, src, p, index, selected)
		out.Close()
		src.Close()
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		output := parseOutput(t, b)
		if len(output.Channels) != 54 || len(output.Programmes) != count {
			t.Fatal("sample export lost channels or programmes")
		}
		if !withMappings && count != 0 {
			t.Fatal("unconfirmed suggestions produced programmes")
		}
		if withMappings && count == 0 {
			t.Fatal("confirmed mappings produced no programmes")
		}
		t.Logf("Provided files: %d channels, %d mappings, %d exported programmes", len(output.Channels), len(selected), count)
	}
}
