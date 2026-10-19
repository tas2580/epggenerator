package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode"
)

type EPGChannel struct {
	ID    string   `json:"id" xml:"id,attr"`
	Names []string `json:"names" xml:"display-name"`
	Icon  struct {
		Src string `json:"src" xml:"src,attr"`
	} `json:"icon" xml:"icon"`
}

type EPGIndex struct {
	Channels   []EPGChannel
	ByID       map[string]EPGChannel
	Programmes int
}

// walkXMLTV holds only one top-level element at a time, never the complete EPG.
func walkXMLTV(r io.Reader, visit func(*xml.Decoder, xml.StartElement) error) error {
	d := xml.NewDecoder(r)
	rootSeen, rootClosed := false, false
	for {
		token, err := d.Token()
		if err == io.EOF {
			if !rootSeen || !rootClosed {
				return fmt.Errorf("EPG: vollständiges <tv>-Element fehlt")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("EPG ist kein gültiges XMLTV: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if !rootSeen {
				if t.Name.Local != "tv" {
					return fmt.Errorf("EPG: erwartet <tv>, erhalten <%s>", t.Name.Local)
				}
				rootSeen = true
			} else {
				if rootClosed {
					return fmt.Errorf("EPG: mehrere XML-Wurzelelemente")
				}
				if err := visit(d, t); err != nil {
					return err
				}
			}
		case xml.EndElement:
			rootClosed = true
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return fmt.Errorf("EPG: unerwarteter Text außerhalb eines Eintrags")
			}
		}
	}
}

func xmlAttribute(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

func readEPG(r io.Reader) (*EPGIndex, error) {
	index := &EPGIndex{ByID: map[string]EPGChannel{}}
	err := walkXMLTV(r, func(d *xml.Decoder, start xml.StartElement) error {
		if start.Name.Local == "channel" {
			var c EPGChannel
			if err := d.DecodeElement(&c, &start); err != nil {
				return err
			}
			if c.ID == "" {
				return fmt.Errorf("EPG: Sender ohne ID")
			}
			if _, exists := index.ByID[c.ID]; exists {
				return fmt.Errorf("EPG: doppelte Sender-ID %q", c.ID)
			}
			if len(c.Names) == 0 {
				c.Names = []string{c.ID}
			}
			index.Channels = append(index.Channels, c)
			index.ByID[c.ID] = c
			return nil
		}
		if start.Name.Local == "programme" {
			if xmlAttribute(start, "channel") == "" {
				return fmt.Errorf("EPG: Sendung ohne Sender-ID")
			}
			index.Programmes++
		}
		return d.Skip()
	})
	if err != nil {
		return nil, err
	}
	if len(index.Channels) == 0 {
		return nil, fmt.Errorf("EPG enthält keine Sender")
	}
	return index, nil
}

type outputChannel struct {
	ID   string      `xml:"id,attr"`
	Name string      `xml:"display-name"`
	Icon *outputIcon `xml:"icon,omitempty"`
}
type outputIcon struct {
	Src string `xml:"src,attr"`
}

func writeEPG(w io.Writer, r io.Reader, playlist *Playlist, index *EPGIndex, mappings map[string]string) (int, error) {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return 0, err
	}
	e := xml.NewEncoder(w)
	root := xml.StartElement{Name: xml.Name{Local: "tv"}, Attr: []xml.Attr{{Name: xml.Name{Local: "generator-info-name"}, Value: "EPG Generator"}}}
	if err := e.EncodeToken(root); err != nil {
		return 0, err
	}
	targets := map[string][]string{}
	for _, c := range playlist.Channels {
		out := outputChannel{ID: c.ID, Name: c.Name}
		logo := c.Logo
		if source, ok := index.ByID[mappings[c.Key]]; ok && !c.Ambiguous {
			targets[source.ID] = append(targets[source.ID], c.ID)
			if logo == "" {
				logo = source.Icon.Src
			}
		}
		if logo != "" {
			out.Icon = &outputIcon{Src: logo}
		}
		if err := e.EncodeElement(out, xml.StartElement{Name: xml.Name{Local: "channel"}}); err != nil {
			return 0, err
		}
	}
	count := 0
	err := walkXMLTV(r, func(d *xml.Decoder, start xml.StartElement) error {
		ids := targets[xmlAttribute(start, "channel")]
		if start.Name.Local != "programme" || len(ids) == 0 {
			return d.Skip()
		}
		// Copy tokens for one programme, preserving all attributes and nested metadata.
		tokens := []xml.Token{}
		depth := 1
		for depth > 0 {
			t, err := d.Token()
			if err != nil {
				return err
			}
			switch t.(type) {
			case xml.StartElement:
				depth++
			case xml.EndElement:
				depth--
			}
			tokens = append(tokens, xml.CopyToken(t))
		}
		for _, id := range ids {
			out := start.Copy()
			for i := range out.Attr {
				if out.Attr[i].Name.Local == "channel" && out.Attr[i].Name.Space == "" {
					out.Attr[i].Value = id
				}
			}
			if err := e.EncodeToken(out); err != nil {
				return err
			}
			for _, t := range tokens {
				if err := e.EncodeToken(t); err != nil {
					return err
				}
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := e.EncodeToken(root.End()); err != nil {
		return 0, err
	}
	return count, e.Flush()
}

func normalizedName(s string, relaxed bool) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, prefix := range []string{"de -", "de-", "de |", "de:"} {
		s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
	}
	if relaxed {
		for _, suffix := range []string{" uhd", " fhd", " hd", " sd"} {
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix))
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

type Suggestion struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func suggestions(c PlaylistChannel, index *EPGIndex) []Suggestion {
	result := []Suggestion{}
	seen := map[string]bool{}
	if _, ok := index.ByID[c.SourceID]; ok {
		result = append(result, Suggestion{c.SourceID, "Gleiche Sender-ID"})
		seen[c.SourceID] = true
	}
	for _, relaxed := range []bool{false, true} {
		name := normalizedName(c.Name, relaxed)
		if name == "" {
			continue
		}
		for _, epg := range index.Channels {
			if seen[epg.ID] {
				continue
			}
			for _, alias := range epg.Names {
				if normalizedName(alias, relaxed) == name {
					reason := "Gleicher Sendername"
					if relaxed {
						reason = "Ähnlicher Name (ohne HD/SD-Zusatz)"
					}
					result = append(result, Suggestion{epg.ID, reason})
					seen[epg.ID] = true
					break
				}
			}
		}
	}
	return result
}
