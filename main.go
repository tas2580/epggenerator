package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func apiError(w http.ResponseWriter, status int, err error) {
	respondJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("Ungültige Anfrage: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("Ungültige Anfrage: nur ein JSON-Objekt erlaubt")
	}
	return nil
}

// Register paths separately from methods so routing also works when the
// process inherits GODEBUG=httpmuxgo121=1 from its environment.
func route(mux *http.ServeMux, method, path string, handler http.HandlerFunc) {
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
			allow := method
			if method == http.MethodGet {
				allow += ", HEAD"
			}
			w.Header().Set("Allow", allow)
			http.Error(w, "Methode nicht erlaubt", http.StatusMethodNotAllowed)
			return
		}
		handler(w, r)
	})
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	route(mux, http.MethodGet, "/api/state", func(w http.ResponseWriter, r *http.Request) { respondJSON(w, http.StatusOK, a.view()) })
	route(mux, http.MethodGet, "/api/logo", a.serveLogo)
	route(mux, http.MethodPatch, "/api/channels", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Channels map[string]*ChannelEdit `json:"channels"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			apiError(w, 400, err)
			return
		}
		if len(input.Channels) == 0 {
			apiError(w, 400, fmt.Errorf("Keine Senderänderungen angegeben"))
			return
		}
		if err := a.updateChannels(input.Channels); err != nil {
			apiError(w, 409, err)
			return
		}
		respondJSON(w, 202, map[string]bool{"accepted": true})
	})
	route(mux, http.MethodPut, "/api/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings Settings
		if err := decodeJSON(w, r, &settings); err != nil {
			apiError(w, 400, err)
			return
		}
		if _, err := validateSettings(settings); err != nil {
			apiError(w, 400, err)
			return
		}
		if err := a.startRefresh(&settings); err != nil {
			apiError(w, 409, err)
			return
		}
		respondJSON(w, 202, map[string]bool{"accepted": true})
	})
	route(mux, http.MethodPost, "/api/refresh", func(w http.ResponseWriter, r *http.Request) {
		if err := a.startRefresh(nil); err != nil {
			apiError(w, 409, err)
			return
		}
		respondJSON(w, 202, map[string]bool{"accepted": true})
	})
	route(mux, http.MethodPatch, "/api/mappings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Mappings map[string]string `json:"mappings"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			apiError(w, 400, err)
			return
		}
		if len(input.Mappings) == 0 {
			apiError(w, 400, fmt.Errorf("Keine Zuordnungen angegeben"))
			return
		}
		if err := a.updateMappings(input.Mappings); err != nil {
			apiError(w, 409, err)
			return
		}
		respondJSON(w, 202, map[string]bool{"accepted": true})
	})
	route(mux, http.MethodGet, "/epg.xml", func(w http.ResponseWriter, r *http.Request) {
		a.serveOutput(w, r, "epg.xml", "application/xml; charset=utf-8")
	})
	route(mux, http.MethodGet, "/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		a.serveOutput(w, r, "playlist.m3u8", "application/vnd.apple.mpegurl")
	})
	web, _ := fs.Sub(assets, "web")
	static := http.FileServer(http.FS(web))
	route(mux, http.MethodGet, "/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" && r.URL.Path != "/channels.css" && r.URL.Path != "/theme.js" && r.URL.Path != "/theme.css" {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// Prevent websites outside the LAN app from changing its settings.
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
					apiError(w, 403, fmt.Errorf("Fremder Ursprung nicht erlaubt"))
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				apiError(w, 403, fmt.Errorf("Fremder Ursprung nicht erlaubt"))
				return
			}
			if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
				apiError(w, 415, fmt.Errorf("Content-Type application/json erforderlich"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *App) serveOutput(w http.ResponseWriter, r *http.Request, name, contentType string) {
	a.mu.RLock()
	gen := a.state.Generation
	if gen == "" {
		a.mu.RUnlock()
		http.Error(w, "Noch keine Ausgabe vorhanden. Bitte Quellen im Webinterface laden.", 503)
		return
	}
	f, err := os.Open(filepath.Join(a.dataDir, gen, name))
	a.mu.RUnlock()
	if err != nil {
		http.Error(w, "Ausgabe konnte nicht geöffnet werden", 500)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "Ausgabe konnte nicht gelesen werden", 500)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	if name == "playlist.m3u8" {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "Playlist konnte nicht gelesen werden", 500)
			return
		}
		lines := strings.SplitN(string(b), "\n", 2)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		epgURL := scheme + "://" + r.Host + "/epg.xml"
		lines[0] = replaceAttribute(replaceAttribute(lines[0], "url-tvg", epgURL), "x-tvg-url", epgURL)
		http.ServeContent(w, r, name, info.ModTime(), strings.NewReader(strings.Join(lines, "\n")))
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func main() {
	configPath := flag.String("config", "config.json", "Konfigurationsdatei (standardmäßig optional im Arbeitsverzeichnis)")
	listen := flag.String("listen", "", "HTTP-Adresse; überschreibt listenPort aus der Konfiguration")
	dataDir := flag.String("data-dir", "./data", "Verzeichnis für Einstellungen und Ausgaben")
	interval := flag.Duration("refresh-interval", 6*time.Hour, "Intervall für automatische Aktualisierungen")
	epgURL := flag.String("epg-url", "", "EPG-URL beim Start setzen (optional)")
	playlistURL := flag.String("playlist-url", "", "Playlist-URL beim Start setzen (optional)")
	playlistFile := flag.String("playlist-file", "", "Lokale Playlist-Datei beim Start setzen (optional)")
	flag.Parse()
	if *playlistURL != "" && *playlistFile != "" {
		log.Fatal("Bitte entweder -playlist-url oder -playlist-file angeben")
	}
	configRequired := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configRequired = true
		}
	})
	listenAddress, err := loadListenAddress(*configPath, configRequired, *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *interval <= 0 {
		log.Fatal("refresh-interval muss positiv sein")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, err := newApp(ctx, *dataDir, *interval)
	if err != nil {
		log.Fatal(err)
	}
	settings := a.state.Settings
	if *epgURL != "" {
		settings.EPGURL = *epgURL
	}
	if *playlistURL != "" {
		settings.PlaylistURL = *playlistURL
		settings.PlaylistFile = ""
	}
	if *playlistFile != "" {
		settings.PlaylistFile = *playlistFile
	}
	if _, err := validateSettings(settings); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: listenAddress, Handler: a.handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Webinterface auf %s; Daten: %s", listenAddress, *dataDir)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
			cancel()
		}
	}()
	if err := a.startRefresh(&settings); err != nil {
		log.Printf("Startaktualisierung: %v", err)
	}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := a.startRefresh(nil); err != nil {
				log.Printf("Automatische Aktualisierung: %v", err)
			}
		case <-ctx.Done():
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(shutdown)
			stop()
			a.wg.Wait()
			select {
			case err := <-serverErrors:
				log.Fatalf("HTTP-Server: %v", err)
			default:
			}
			return
		}
	}
}
