package engine

import (
	"strings"
	"testing"

	"git.nekocwd.link/neko/persia/internal/forge"
	"git.nekocwd.link/neko/persia/internal/message"
	"git.nekocwd.link/neko/persia/internal/store"
)

// testMessages loads the templates shipped in the repository.
func testMessages(t *testing.T) *message.Set {
	t.Helper()
	set, err := message.Load("../../messages")
	if err != nil {
		t.Fatalf("cannot load message templates: %v", err)
	}
	return set
}

func render(t *testing.T, set *message.Set, event string, data message.Data) (string, string) {
	t.Helper()
	msg, err := set.Render(event, data)
	if err != nil {
		t.Fatalf("cannot render %s: %v", event, err)
	}
	return msg.Text, msg.HTML
}

func TestNewIssueMessageEscapesHTML(t *testing.T) {
	set := testMessages(t)
	text, markup := render(t, set, "new_issue", newIssueData("github", "alice/demo", forge.Issue{
		Kind:    "issue",
		Number:  7,
		Title:   `<script>alert("x")</script>`,
		HTMLURL: "https://github.com/alice/demo/issues/7?x=1&y=2",
	}))
	if strings.Contains(markup, "<script>") {
		t.Fatalf("HTML was not escaped: %s", markup)
	}
	if !strings.Contains(markup, "&lt;script&gt;") {
		t.Fatalf("expected escaped title in HTML: %s", markup)
	}
	if !strings.Contains(text, "#7") {
		t.Fatalf("expected issue number in text: %s", text)
	}
}

func TestUpdatedIssueMessageListsChanges(t *testing.T) {
	set := testMessages(t)
	text, _ := render(t, set, "updated_issue", updatedIssueData("local", "alice/demo",
		store.Item{State: "open", Title: "Old", Comments: 1},
		forge.Issue{Kind: "pull_request", Number: 3, Title: "New", State: "merged", Comments: 4, HTMLURL: "https://example.org/3"},
	))
	for _, want := range []string{"open → merged", "Old → New", "1 → 4"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in %q", want, text)
		}
	}
	if !strings.Contains(text, "pull request") {
		t.Errorf("expected the kind label in %q", text)
	}
}

func TestPipelineFailedMessage(t *testing.T) {
	set := testMessages(t)
	text, markup := render(t, set, "pipeline_failed", pipelineFailedData("github", "alice/demo", forge.Pipeline{
		ID:     "1",
		Name:   "CI",
		Status: "failure",
		URL:    "https://example.org/run/1",
		Ref:    "main",
		SHA:    "0123456789abcdef",
	}))
	if !strings.Contains(text, "CI failed") {
		t.Fatalf("unexpected text: %s", text)
	}
	if !strings.Contains(markup, "01234567") {
		t.Fatalf("short sha missing: %s", markup)
	}
	if strings.Contains(markup, "0123456789abcdef") {
		t.Fatalf("sha was not shortened: %s", markup)
	}
}

func TestMirrorMessages(t *testing.T) {
	set := testMessages(t)

	text, markup := render(t, set, "mirror_failed", message.Data{
		Repo:   "acme/widget",
		Target: "github",
		Error:  "remote rejected: <boom>",
	})
	if !strings.Contains(text, "Push mirror failed") || !strings.Contains(text, "github") {
		t.Errorf("unexpected text: %s", text)
	}
	if strings.Contains(markup, "<boom>") {
		t.Errorf("error text was not escaped: %s", markup)
	}

	text, _ = render(t, set, "mirror_recovered", message.Data{Repo: "acme/widget", Target: "github"})
	if !strings.Contains(text, "recovered") {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestEveryEventRenders(t *testing.T) {
	set := testMessages(t)
	data := message.Data{
		Repo:     "acme/widget",
		Source:   "github",
		Target:   "github",
		URL:      "https://example.org/1",
		RepoURL:  "https://git.example.org/acme/widget",
		RoomID:   "!room:example.org",
		KeyTitle: "persia widget",
		Kind:     "issue",
		Number:   1,
		Title:    "Title",
		Body:     "Body",
		Name:     "CI",
		Ref:      "main",
		SHA:      "0123456789abcdef",
		Status:   "failure",
		Error:    "boom",
		Changes:  []string{"state: open → closed"},
	}

	for _, event := range message.Events {
		text, markup := render(t, set, event, data)
		if strings.TrimSpace(text) == "" && strings.TrimSpace(markup) == "" {
			t.Errorf("%s rendered empty text and html", event)
		}
	}
}
