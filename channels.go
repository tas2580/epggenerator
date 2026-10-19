package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ChannelEdit struct {
	Name  *string `json:"name,omitempty"`
	Group *string `json:"group,omitempty"`
}

func editedPlaylist(source *Playlist, edits map[string]ChannelEdit) *Playlist {
	p := &Playlist{Lines: append([]string(nil), source.Lines...), Channels: append([]PlaylistChannel(nil), source.Channels...)}
	for i := range p.Channels {
		c := &p.Channels[i]
		edit, ok := edits[c.Key]
		if !ok || c.Ambiguous {
			continue
		}
		line := p.Lines[c.Line]
		if edit.Name != nil {
			c.Name = *edit.Name
			line = line[:metadataEnd(line)+1] + c.Name
			line = replaceAttribute(line, "tvg-name", c.Name)
		}
		if edit.Group != nil {
			c.Group = *edit.Group
			line = replaceAttribute(line, "group-title", c.Group)
			for j := c.Line + 1; j < len(p.Lines); j++ {
				trimmed := strings.TrimSpace(p.Lines[j])
				if strings.HasPrefix(trimmed, "#EXTINF:") {
					break
				}
				if strings.HasPrefix(trimmed, "#EXTGRP:") {
					p.Lines[j] = "#EXTGRP:" + c.Group
				}
			}
		}
		p.Lines[c.Line] = line
	}
	return p
}

func validateChannelText(value string, name bool) (string, error) {
	if strings.ContainsAny(value, "\r\n\"") || strings.IndexFunc(value, unicode.IsControl) >= 0 || !utf8.ValidString(value) {
		return "", fmt.Errorf("Sendername und Kategorie dürfen keine Steuerzeichen oder doppelten Anführungszeichen enthalten")
	}
	value = strings.TrimSpace(value)
	if name && value == "" {
		return "", fmt.Errorf("Bitte einen Sendernamen eingeben")
	}
	if utf8.RuneCountInString(value) > 200 {
		return "", fmt.Errorf("Sendername und Kategorie dürfen höchstens 200 Zeichen enthalten")
	}
	return value, nil
}

func (a *App) updateChannels(changes map[string]*ChannelEdit) error {
	state, err := a.begin()
	if err != nil {
		return err
	}
	a.mu.RLock()
	p, index := a.playlist, a.epg
	a.mu.RUnlock()
	fail := func(err error) error { a.finish(err); return err }
	if p == nil {
		return fail(fmt.Errorf("Bitte zuerst die Quellen laden"))
	}
	channels := make(map[string]PlaylistChannel, len(p.Channels))
	for _, c := range p.Channels {
		channels[c.Key] = c
	}
	for key, change := range changes {
		c, ok := channels[key]
		if !ok {
			return fail(fmt.Errorf("Playlist-Sender existiert nicht mehr. Bitte Ansicht aktualisieren."))
		}
		if change == nil {
			delete(state.ChannelEdits, key)
			continue
		}
		if c.Ambiguous {
			return fail(fmt.Errorf("Sender %q ist nicht eindeutig. Bitte eine eindeutige tvg-id in der Playlist ergänzen.", c.Name))
		}
		edit := state.ChannelEdits[key]
		if change.Name != nil {
			name, err := validateChannelText(*change.Name, true)
			if err != nil {
				return fail(err)
			}
			edit.Name = nil
			if name != c.Name {
				edit.Name = &name
			}
		}
		if change.Group != nil {
			group, err := validateChannelText(*change.Group, false)
			if err != nil {
				return fail(err)
			}
			edit.Group = nil
			if group != c.Group {
				edit.Group = &group
			}
		}
		if edit.Name == nil && edit.Group == nil {
			delete(state.ChannelEdits, key)
		} else {
			state.ChannelEdits[key] = edit
		}
	}
	go func() { a.finish(a.regenerate(state, p, index)) }()
	return nil
}
