package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type PlaylistChannel struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	SourceID  string `json:"sourceId"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	Logo      string `json:"logo"`
	URL       string `json:"-"`
	Ambiguous bool   `json:"ambiguous"`
	Line      int    `json:"-"`
}

type Playlist struct {
	Lines    []string
	Channels []PlaylistChannel
}

var attributePattern = regexp.MustCompile(`([\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

func attributes(line string) map[string]string {
	result := make(map[string]string)
	for _, m := range attributePattern.FindAllStringSubmatch(line, -1) {
		value := m[2]
		if m[3] != "" {
			value = m[3]
		}
		result[strings.ToLower(m[1])] = value
	}
	return result
}

func metadataEnd(line string) int {
	var quote rune
	for i, c := range line {
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == ',' {
			return i
		}
	}
	return -1
}

func shortHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))[:24] }

func parsePlaylist(r io.Reader) (*Playlist, error) {
	p := &Playlist{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 65536), 2*1024*1024)
	pending := -1
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if len(p.Lines) == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		i := len(p.Lines)
		p.Lines = append(p.Lines, line)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#EXTINF:") {
			if pending >= 0 {
				return nil, fmt.Errorf("Playlist: Stream-Adresse fehlt vor Zeile %d", i+1)
			}
			comma := metadataEnd(trimmed)
			if comma < 0 {
				return nil, fmt.Errorf("Playlist: ungültiger EXTINF-Eintrag in Zeile %d", i+1)
			}
			a := attributes(trimmed[:comma])
			name := strings.TrimSpace(trimmed[comma+1:])
			if name == "" {
				name = a["tvg-name"]
			}
			if name == "" {
				name = a["tvg-id"]
			}
			if name == "" {
				name = fmt.Sprintf("Sender %d", len(p.Channels)+1)
			}
			p.Channels = append(p.Channels, PlaylistChannel{SourceID: a["tvg-id"], Name: name, Group: a["group-title"], Logo: a["tvg-logo"], Line: i})
			pending = len(p.Channels) - 1
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if pending < 0 {
				return nil, fmt.Errorf("Playlist: Stream ohne EXTINF in Zeile %d", i+1)
			}
			p.Channels[pending].URL = trimmed
			pending = -1
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(p.Lines) == 0 || !strings.HasPrefix(strings.TrimSpace(p.Lines[0]), "#EXTM3U") {
		return nil, fmt.Errorf("Playlist: #EXTM3U-Kopf fehlt")
	}
	if pending >= 0 {
		return nil, fmt.Errorf("Playlist: letzter Sender hat keine Stream-Adresse")
	}
	if len(p.Channels) == 0 {
		return nil, fmt.Errorf("Playlist enthält keine Sender")
	}
	ids, urls := map[string]int{}, map[string]int{}
	for _, c := range p.Channels {
		ids[c.SourceID]++
		urls[c.URL]++
	}
	used, occurrences := map[string]bool{}, map[string]int{}
	for id := range ids {
		if id != "" {
			used[id] = true
		}
	}
	for i := range p.Channels {
		c := &p.Channels[i]
		if c.SourceID != "" && ids[c.SourceID] == 1 {
			c.Key, c.ID = "id:"+c.SourceID, c.SourceID
		} else {
			c.Key = "stream:" + shortHash(c.URL)
			if urls[c.URL] > 1 {
				c.Ambiguous = true
				occurrences[c.URL]++
				c.Key += fmt.Sprintf(":%d", occurrences[c.URL])
			}
			base := "epggenerator." + shortHash(c.Key)
			c.ID = base
			for suffix := 2; used[c.ID]; suffix++ {
				c.ID = fmt.Sprintf("%s.%d", base, suffix)
			}
			used[c.ID] = true
		}
	}
	return p, nil
}

func replaceAttribute(line, key, value string) string {
	// Only change metadata before the display-name separator.
	end := metadataEnd(line)
	if end < 0 {
		end = len(line)
	}
	head, tail := line[:end], line[end:]
	pattern := regexp.MustCompile(`(?i)\s+` + regexp.QuoteMeta(key) + `\s*=\s*(?:"[^"]*"|'[^']*')`)
	head = pattern.ReplaceAllString(head, "")
	return head + " " + key + `="` + strings.ReplaceAll(value, `"`, "&quot;") + `"` + tail
}

func (p *Playlist) Write(w io.Writer) error {
	lines := append([]string(nil), p.Lines...)
	for _, c := range p.Channels {
		lines[c.Line] = replaceAttribute(lines[c.Line], "tvg-id", c.ID)
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
	return err
}
