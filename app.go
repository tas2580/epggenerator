package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Settings struct {
	EPGURL       string `json:"epgUrl"`
	PlaylistURL  string `json:"playlistUrl"`
	PlaylistFile string `json:"playlistFile"`
}

type persistedState struct {
	Settings         Settings               `json:"settings"`
	Mappings         map[string]string      `json:"mappings"`
	ChannelEdits     map[string]ChannelEdit `json:"channelEdits,omitempty"`
	Generation       string                 `json:"generation"`
	LastRefresh      time.Time              `json:"lastRefresh"`
	LastGenerated    time.Time              `json:"lastGenerated"`
	OutputProgrammes int                    `json:"outputProgrammes"`
	LastError        string                 `json:"lastError"`
}

type App struct {
	mu       sync.RWMutex
	state    persistedState
	playlist *Playlist
	epg      *EPGIndex
	busy     bool
	dataDir  string
	client   *http.Client
	ctx      context.Context
	wg       sync.WaitGroup
	interval time.Duration
}

func newApp(ctx context.Context, dataDir string, interval time.Duration) (*App, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	a := &App{ctx: ctx, dataDir: dataDir, interval: interval, client: &http.Client{Timeout: 3 * time.Minute}}
	a.state = persistedState{Settings: Settings{EPGURL: "https://iptv-epg.org/files/epg-de.xml", PlaylistURL: "http://tv.lan/tv.m3u8"}, Mappings: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dataDir, "state.json"))
	if err == nil {
		if err := json.Unmarshal(b, &a.state); err != nil {
			return nil, fmt.Errorf("Einstellungen lesen: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if a.state.Mappings == nil {
		a.state.Mappings = map[string]string{}
	}
	if a.state.ChannelEdits == nil {
		a.state.ChannelEdits = map[string]ChannelEdit{}
	}
	if gen := a.state.Generation; gen != "" {
		if filepath.Base(gen) != gen || !strings.HasPrefix(gen, "generation-") {
			return nil, fmt.Errorf("Ungültiges Ausgabeverzeichnis")
		}
		a.playlist, a.epg, err = loadSources(filepath.Join(dataDir, gen))
		if err != nil {
			return nil, fmt.Errorf("Gespeicherte Quellen laden: %w", err)
		}
	}
	// Generations are immutable. Only state.json selects the active generation.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "generation-") && entry.Name() != a.state.Generation {
			_ = os.RemoveAll(filepath.Join(dataDir, entry.Name()))
		}
	}
	return a, nil
}

func validateSettings(s Settings) (Settings, error) {
	s.EPGURL, s.PlaylistURL = strings.TrimSpace(s.EPGURL), strings.TrimSpace(s.PlaylistURL)
	s.PlaylistFile = strings.TrimSpace(s.PlaylistFile)
	urls := map[string]string{"EPG-URL": s.EPGURL}
	if s.PlaylistFile == "" {
		urls["Playlist-URL"] = s.PlaylistURL
	} else {
		// Persist absolute paths so a later restart in another directory does not
		// silently read a different playlist.
		path, err := filepath.Abs(s.PlaylistFile)
		if err != nil {
			return s, fmt.Errorf("Playlist-Dateipfad: %w", err)
		}
		s.PlaylistFile = path
	}
	for name, value := range urls {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Fragment != "" {
			return s, fmt.Errorf("%s muss eine vollständige HTTP- oder HTTPS-Adresse ohne Fragment sein", name)
		}
	}
	return s, nil
}

func saveState(dir string, state persistedState) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "state.json"))
}

func cloneState(s persistedState) persistedState {
	mappings := make(map[string]string, len(s.Mappings))
	for k, v := range s.Mappings {
		mappings[k] = v
	}
	s.Mappings = mappings
	edits := make(map[string]ChannelEdit, len(s.ChannelEdits))
	for key, edit := range s.ChannelEdits {
		edits[key] = edit
	}
	s.ChannelEdits = edits
	return s
}

func loadSources(dir string) (*Playlist, *EPGIndex, error) {
	f, err := os.Open(filepath.Join(dir, "source.m3u8"))
	if err != nil {
		return nil, nil, err
	}
	p, err := parsePlaylist(f)
	f.Close()
	if err != nil {
		return nil, nil, err
	}
	f, err = os.Open(filepath.Join(dir, "source.xml"))
	if err != nil {
		return nil, nil, err
	}
	index, err := readEPG(f)
	f.Close()
	return p, index, err
}

func (a *App) download(rawURL, dest string, limit int64) error {
	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "EPGGenerator/1.0")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Quelle antwortet mit HTTP %d", resp.StatusCode)
	}
	return copySource(resp.Body, dest, limit)
}

func copySource(source io.Reader, dest string, limit int64) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(source, limit+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > limit {
		return fmt.Errorf("Quelldatei überschreitet das Größenlimit von %d MB", limit/(1024*1024))
	}
	return nil
}

func (a *App) loadPlaylistSource(settings Settings, dest string) error {
	const limit = 20 * 1024 * 1024
	if settings.PlaylistFile == "" {
		return a.download(settings.PlaylistURL, dest, limit)
	}
	info, err := os.Stat(settings.PlaylistFile)
	if err != nil {
		return fmt.Errorf("Lokale Playlist: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Lokale Playlist muss eine reguläre Datei sein: %s", settings.PlaylistFile)
	}
	if info.Size() > limit {
		return fmt.Errorf("Lokale Playlist überschreitet das Größenlimit von 20 MB")
	}
	f, err := os.Open(settings.PlaylistFile)
	if err != nil {
		return fmt.Errorf("Lokale Playlist öffnen: %w", err)
	}
	defer f.Close()
	return copySource(f, dest, limit)
}

func generate(dir string, p *Playlist, index *EPGIndex, mappings map[string]string) (int, error) {
	source, err := os.Open(filepath.Join(dir, "source.xml"))
	if err != nil {
		return 0, err
	}
	defer source.Close()
	out, err := os.Create(filepath.Join(dir, "epg.xml"))
	if err != nil {
		return 0, err
	}
	count, err := writeEPG(out, source, p, index, mappings)
	closeErr := out.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	out, err = os.Create(filepath.Join(dir, "playlist.m3u8"))
	if err != nil {
		return 0, err
	}
	err = p.Write(out)
	closeErr = out.Close()
	if err != nil {
		return 0, err
	}
	return count, closeErr
}

func (a *App) begin() (persistedState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return persistedState{}, fmt.Errorf("Dienst wird beendet")
	}
	if a.busy {
		return persistedState{}, fmt.Errorf("Eine Aktualisierung läuft bereits")
	}
	a.busy = true
	a.wg.Add(1)
	return cloneState(a.state), nil
}

func (a *App) finish(err error) {
	a.mu.Lock()
	if err != nil {
		a.state.LastError = err.Error()
		if saveErr := saveState(a.dataDir, a.state); saveErr != nil {
			log.Printf("Fehlerstatus speichern: %v", saveErr)
		}
		log.Printf("Aktualisierung fehlgeschlagen: %v", err)
	}
	a.busy = false
	a.mu.Unlock()
	a.wg.Done()
}

func (a *App) commit(state persistedState, dir string, p *Playlist, index *EPGIndex, count int) error {
	state.Generation = filepath.Base(dir)
	state.LastGenerated = time.Now().UTC()
	state.OutputProgrammes = count
	state.LastError = ""
	if err := saveState(a.dataDir, state); err != nil {
		return err
	}
	a.mu.Lock()
	previous := a.state.Generation
	a.state, a.playlist, a.epg = state, p, index
	a.mu.Unlock()
	if previous != "" && previous != state.Generation {
		_ = os.RemoveAll(filepath.Join(a.dataDir, previous))
	}
	log.Printf("Ausgabe aktualisiert: %d Sender, %d Sendungen", len(p.Channels), count)
	return nil
}

func (a *App) refresh(state persistedState) error {
	dir, err := os.MkdirTemp(a.dataDir, "generation-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := a.loadPlaylistSource(state.Settings, filepath.Join(dir, "source.m3u8")); err != nil {
		return fmt.Errorf("Playlist laden: %w", err)
	}
	if err := a.download(state.Settings.EPGURL, filepath.Join(dir, "source.xml"), 512*1024*1024); err != nil {
		return fmt.Errorf("EPG laden: %w", err)
	}
	p, index, err := loadSources(dir)
	if err != nil {
		return err
	}
	count, err := generate(dir, editedPlaylist(p, state.ChannelEdits), index, state.Mappings)
	if err != nil {
		return err
	}
	state.LastRefresh = time.Now().UTC()
	if err := a.commit(state, dir, p, index, count); err != nil {
		return err
	}
	committed = true
	return nil
}

func (a *App) startRefresh(settings *Settings) error {
	state, err := a.begin()
	if err != nil {
		return err
	}
	if settings != nil {
		validated, err := validateSettings(*settings)
		if err != nil {
			a.finish(err)
			return err
		}
		state.Settings = validated
		if err := saveState(a.dataDir, state); err != nil {
			a.finish(err)
			return err
		}
		a.mu.Lock()
		a.state.Settings = validated
		a.mu.Unlock()
	}
	go func() { a.finish(a.refresh(state)) }()
	return nil
}

func (a *App) updateMappings(changes map[string]string) error {
	state, err := a.begin()
	if err != nil {
		return err
	}
	a.mu.RLock()
	p, index := a.playlist, a.epg
	a.mu.RUnlock()
	if p == nil {
		err := fmt.Errorf("Bitte zuerst die Quellen laden")
		a.finish(err)
		return err
	}
	keys := map[string]PlaylistChannel{}
	for _, c := range p.Channels {
		keys[c.Key] = c
	}
	for key, id := range changes {
		c, ok := keys[key]
		if !ok {
			err = fmt.Errorf("Playlist-Sender existiert nicht mehr. Bitte Ansicht aktualisieren.")
			break
		}
		if id != "" {
			if c.Ambiguous {
				err = fmt.Errorf("Sender %q hat keine eindeutige ID oder Stream-Adresse. Bitte Playlist korrigieren.", c.Name)
				break
			}
			if _, ok := index.ByID[id]; !ok {
				err = fmt.Errorf("EPG-Sender %q existiert nicht", id)
				break
			}
			state.Mappings[key] = id
		} else {
			delete(state.Mappings, key)
		}
	}
	if err != nil {
		a.finish(err)
		return err
	}
	go func() { a.finish(a.regenerate(state, p, index)) }()
	return nil
}

func (a *App) regenerate(state persistedState, p *Playlist, index *EPGIndex) error {
	dir, err := os.MkdirTemp(a.dataDir, "generation-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, name := range []string{"source.xml", "source.m3u8"} {
		if err := os.Link(filepath.Join(a.dataDir, state.Generation, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	count, err := generate(dir, editedPlaylist(p, state.ChannelEdits), index, state.Mappings)
	if err != nil {
		return err
	}
	if err := a.commit(state, dir, p, index, count); err != nil {
		return err
	}
	committed = true
	return nil
}

type ChannelView struct {
	PlaylistChannel
	Mapping       string       `json:"mapping"`
	Status        string       `json:"status"`
	Suggestions   []Suggestion `json:"suggestions"`
	OriginalName  string       `json:"originalName"`
	OriginalGroup string       `json:"originalGroup"`
	Edited        bool         `json:"edited"`
}

func (a *App) view() any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	channels := []ChannelView{}
	epgChannels := []EPGChannel{}
	mapped, review := 0, 0
	if a.playlist != nil {
		epgChannels = a.epg.Channels
		for _, c := range a.playlist.Channels {
			v := ChannelView{PlaylistChannel: c, Mapping: a.state.Mappings[c.Key], Status: "unmapped", Suggestions: suggestions(c, a.epg)}
			v.OriginalName, v.OriginalGroup = c.Name, c.Group
			if edit, ok := a.state.ChannelEdits[c.Key]; ok && !c.Ambiguous {
				if edit.Name != nil {
					v.Name = *edit.Name
				}
				if edit.Group != nil {
					v.Group = *edit.Group
				}
				v.Edited = true
			}
			v.Logo = channelLogo(c, a.epg, a.state.Mappings)
			if c.Ambiguous {
				v.Status = "ambiguous"
				review++
			} else if v.Mapping != "" {
				if _, ok := a.epg.ByID[v.Mapping]; ok {
					v.Status = "mapped"
					mapped++
				} else {
					v.Status = "missing"
					review++
				}
			}
			channels = append(channels, v)
		}
	}
	return struct {
		Settings         Settings      `json:"settings"`
		Channels         []ChannelView `json:"channels"`
		EPGChannels      []EPGChannel  `json:"epgChannels"`
		Busy             bool          `json:"busy"`
		LastError        string        `json:"lastError"`
		LastRefresh      time.Time     `json:"lastRefresh"`
		LastGenerated    time.Time     `json:"lastGenerated"`
		OutputProgrammes int           `json:"outputProgrammes"`
		Mapped           int           `json:"mapped"`
		Review           int           `json:"review"`
		HasOutput        bool          `json:"hasOutput"`
		IntervalSeconds  int64         `json:"intervalSeconds"`
	}{a.state.Settings, channels, epgChannels, a.busy, a.state.LastError, a.state.LastRefresh, a.state.LastGenerated, a.state.OutputProgrammes, mapped, review, a.state.Generation != "", int64(a.interval.Seconds())}
}
