package message

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSet writes a complete template set into dir, letting the caller
// override individual files.
func writeSet(t *testing.T, dir string, override map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, event := range Events {
		content, ok := override[event]
		if !ok {
			content = `{{define "text"}}` + event + ` text{{end}}` +
				`{{define "html"}}` + event + ` html{{end}}`
		}
		path := filepath.Join(dir, event+".tmpl")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRequiresDirectory(t *testing.T) {
	if _, err := Load(""); err == nil {
		t.Error("expected an error for an empty directory")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error for a missing directory")
	}
}

func TestLoadRequiresEveryEvent(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, nil)
	if err := os.Remove(filepath.Join(dir, "mirror_failed.tmpl")); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error for a missing template")
	}
	if !strings.Contains(err.Error(), "mirror_failed.tmpl") {
		t.Errorf("error should name the missing file: %v", err)
	}
}

func TestLoadRequiresTextAndHTML(t *testing.T) {
	for _, missing := range []string{"text", "html"} {
		t.Run("missing "+missing, func(t *testing.T) {
			dir := t.TempDir()
			writeSet(t, dir, map[string]string{
				"new_issue": `{{define "text"}}only text{{end}}`,
			})
			// Remove the html definition from the overridden template.
			if missing == "text" {
				writeSet(t, dir, map[string]string{
					"new_issue": `{{define "html"}}only html{{end}}`,
				})
			}

			_, err := Load(dir)
			if err == nil {
				t.Fatalf("expected an error when %q is missing", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("error should mention %q: %v", missing, err)
			}
		})
	}
}

func TestLoadRejectsInvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, map[string]string{
		"new_issue": `{{define "text"}}{{.Title{{end}}{{define "html"}}x{{end}}`,
	})

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected an error for an invalid template")
	}
	if !strings.Contains(err.Error(), "new_issue.tmpl") {
		t.Errorf("error should name the broken file: %v", err)
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, map[string]string{
		"new_issue": `{{define "text"}}{{.Title}}{{end}}` +
			`{{define "html"}}<b>{{.Title}}</b>{{end}}`,
	})
	set, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := set.Render("new_issue", Data{Title: `<script>x</script>`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg.HTML, "<script>") {
		t.Errorf("html template output was not escaped: %s", msg.HTML)
	}
	if !strings.Contains(msg.Text, "<script>x</script>") {
		t.Errorf("plain text should keep the raw value: %s", msg.Text)
	}
}

func TestRenderUnknownEvent(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, nil)
	set, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Render("nope", Data{}); err == nil {
		t.Error("expected an error for an unknown event")
	}
}

func TestShippedTemplatesLoad(t *testing.T) {
	set, err := Load("../../messages")
	if err != nil {
		t.Fatalf("shipped templates are invalid: %v", err)
	}
	for _, event := range Events {
		if _, err := set.Render(event, Data{}); err != nil {
			t.Errorf("event %s does not render with empty data: %v", event, err)
		}
	}
}

func TestHelpers(t *testing.T) {
	if got := shorten("0123456789abcdef", 8); got != "01234567" {
		t.Errorf("shorten = %q", got)
	}
	if got := shorten("abc", 8); got != "abc" {
		t.Errorf("shorten should keep short strings, got %q", got)
	}
	if got := join(", ", []string{"a", "b"}); got != "a, b" {
		t.Errorf("join = %q", got)
	}
	if got := kindLabel("pull_request"); got != "pull request" {
		t.Errorf("kindLabel = %q", got)
	}
	if got := kindLabel("issue"); got != "issue" {
		t.Errorf("kindLabel = %q", got)
	}

	long := strings.Repeat("word ", 200)
	got := excerpt(100, long)
	if len([]rune(got)) > 101 {
		t.Errorf("excerpt too long: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated excerpt should end with an ellipsis: %q", got)
	}
	if excerpt(100, "  ") != "" {
		t.Error("empty bodies should produce an empty excerpt")
	}
}
