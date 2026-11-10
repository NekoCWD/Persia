// Package message renders Matrix notifications from configurable templates.
//
// Every user-visible string lives in a template file on disk, so changing the
// wording never requires a rebuild. Persia refuses to start when the messages
// directory is missing, incomplete or contains an invalid template.
package message

import (
	"bytes"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"strings"
	texttemplate "text/template"

	"git.nekocwd.link/neko/persia/internal/matrix"
)

// Events lists every notification template Persia requires. Each event maps
// to <event>.tmpl in the messages directory, which must define both a "text"
// and an "html" template.
var Events = []string{
	"new_issue",
	"updated_issue",
	"pipeline_failed",
	"pipeline_recovered",
	"mirror_failed",
	"mirror_recovered",
	"room_created",
	"room_retired",
	"room_initialized",
	"room_provisioned",
	"mirror_recreated",
	"deploy_key_added",
	"deploy_key_exists_as_user",
	"deploy_key_removed_stale",
	"deploy_key_removed_unmanage",
	"repo_retired",
}

// Data is the input available to every message template. Only the fields
// relevant for a given event are populated; the rest stay at their zero value.
type Data struct {
	// Common repository fields.
	Repo   string
	Source string
	Target string
	URL    string

	// Issues and pull requests.
	Kind    string
	Number  int
	Title   string
	Body    string
	Changes []string

	// Pipelines.
	Name   string
	Ref    string
	SHA    string
	Status string

	// Push mirrors.
	Error string

	// Rooms, deploy keys and the management log.
	RepoURL      string
	RoomID       string
	KeyTitle     string
	SyncOnCommit bool

	// TargetData carries the free-form `template_data` of the target that
	// produced the event, so templates can render target-specific labels,
	// links or badges without code changes. Empty when the event has no
	// target (local Forgejo, room lifecycle).
	TargetData map[string]string
}

// Set is a loaded collection of notification templates.
type Set struct {
	text map[string]*texttemplate.Template
	html map[string]*htmltemplate.Template
}

// Load reads <event>.tmpl files from dir and parses them. It returns an error
// naming every missing or invalid template instead of failing on the first.
func Load(dir string) (*Set, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("matrix.messages_dir is required")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("messages directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("messages directory %q is not a directory", dir)
	}

	set := &Set{
		text: make(map[string]*texttemplate.Template, len(Events)),
		html: make(map[string]*htmltemplate.Template, len(Events)),
	}

	var errs []error
	for _, event := range Events {
		path := filepath.Join(dir, event+".tmpl")
		if _, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("message template %s: %w", path, err))
			continue
		}

		parsedText, err := texttemplate.New(event).Funcs(textFuncs()).ParseFiles(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", path, err))
			continue
		}
		text := parsedText.Lookup("text")
		if text == nil {
			errs = append(errs, fmt.Errorf("%s: missing {{define \"text\"}}", path))
			continue
		}

		parsedHTML, err := htmltemplate.New(event).Funcs(htmlFuncs()).ParseFiles(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse %s: %w", path, err))
			continue
		}
		markup := parsedHTML.Lookup("html")
		if markup == nil {
			errs = append(errs, fmt.Errorf("%s: missing {{define \"html\"}}", path))
			continue
		}

		set.text[event] = text
		set.html[event] = markup
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return set, nil
}

// Render executes the text and html templates for an event.
func (s *Set) Render(event string, data Data) (matrix.Message, error) {
	text, ok := s.text[event]
	if !ok {
		return matrix.Message{}, fmt.Errorf("unknown message event %q", event)
	}
	markup, ok := s.html[event]
	if !ok {
		return matrix.Message{}, fmt.Errorf("unknown message event %q", event)
	}

	var plain bytes.Buffer
	if err := text.Execute(&plain, data); err != nil {
		return matrix.Message{}, fmt.Errorf("render %s text: %w", event, err)
	}
	var formatted bytes.Buffer
	if err := markup.Execute(&formatted, data); err != nil {
		return matrix.Message{}, fmt.Errorf("render %s html: %w", event, err)
	}

	return matrix.Message{
		Text: strings.TrimRight(plain.String(), "\n"),
		HTML: strings.TrimRight(formatted.String(), "\n"),
	}, nil
}

func textFuncs() texttemplate.FuncMap {
	return texttemplate.FuncMap{
		"join":        join,
		"kind":        kindLabel,
		"excerpt":     excerpt,
		"short":       shorten,
		"sourceLabel": sourceLabel,
		"targetData":  targetData,
	}
}

func htmlFuncs() htmltemplate.FuncMap {
	return htmltemplate.FuncMap{
		"join":        join,
		"kind":        kindLabel,
		"excerpt":     excerpt,
		"short":       shorten,
		"sourceLabel": sourceLabel,
		"targetData":  targetData,
	}
}

// sourceLabel renders the event source using the target's "label" template
// data when configured, otherwise the raw source name. It lets a target show
// a friendlier name (for example "GitHub" instead of "github") without
// changing the templates.
func sourceLabel(data Data) string {
	if label := strings.TrimSpace(data.TargetData["label"]); label != "" {
		return label
	}
	return data.Source
}

// targetData returns an arbitrary key from the target's template_data map.
func targetData(data Data, key string) string {
	return data.TargetData[key]
}

// join is strings.Join with the separator first, which reads better inside
// templates: {{join ", " .Changes}}.
func join(sep string, values []string) string {
	return strings.Join(values, sep)
}

// kindLabel renders the issue kind for humans.
func kindLabel(kind string) string {
	if kind == "pull_request" {
		return "pull request"
	}
	return "issue"
}

// shorten cuts a string to at most limit runes.
func shorten(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// excerpt trims a long body to at most limit runes, cutting at the last word
// boundary so the result does not end mid-word.
func excerpt(limit int, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	runes := []rune(body)
	if len(runes) > limit {
		runes = runes[:limit]
		body = strings.TrimSpace(string(runes))
		if idx := strings.LastIndex(body, " "); idx > 0 {
			body = strings.TrimSpace(body[:idx])
		}
		return body + "…"
	}
	return body
}
