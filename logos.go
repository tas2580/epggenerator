package main

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func channelLogo(c PlaylistChannel, index *EPGIndex, mappings map[string]string) string {
	if c.Logo != "" {
		return c.Logo
	}
	if source, ok := index.ByID[mappings[c.Key]]; ok && !c.Ambiguous {
		return source.Icon.Src
	}
	return ""
}

// Preview only logos belonging to imported channels; never accept a caller's URL.
func (a *App) serveLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a.mu.RLock()
	logo := ""
	if a.playlist != nil {
		for _, c := range a.playlist.Channels {
			if c.Key == r.URL.Query().Get("key") {
				logo = channelLogo(c, a.epg, a.state.Mappings)
				break
			}
		}
	}
	a.mu.RUnlock()
	if logo == "" {
		http.Error(w, "Kein Senderlogo vorhanden", http.StatusNotFound)
		return
	}
	u, err := url.Parse(logo)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		http.Error(w, "Senderlogo benötigt eine HTTP- oder HTTPS-Adresse", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logo, nil)
	if err != nil {
		http.Error(w, "Ungültige Logo-Adresse", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "EPGGenerator/1.0")
	resp, err := a.client.Do(req)
	if err != nil {
		http.Error(w, "Logo-Quelle nicht erreichbar", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Logo-Quelle liefert kein Bild", http.StatusBadGateway)
		return
	}
	const limit = 5 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) == 0 || len(data) > limit {
		http.Error(w, "Logo nicht lesbar oder größer als 5 MB", http.StatusBadGateway)
		return
	}
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "image/") {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(contentType, "image/") {
		http.Error(w, "Logo-Quelle liefert keine Bilddatei", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
