package engine

import (
	"fmt"
	"strings"

	"git.nekocwd.link/neko/persia/internal/forge"
	"git.nekocwd.link/neko/persia/internal/message"
	"git.nekocwd.link/neko/persia/internal/store"
)

// All user-visible wording lives in internal/message templates; these helpers
// only map engine data onto template fields.

func newIssueData(source, repo string, item forge.Issue) message.Data {
	return message.Data{
		Repo:   repo,
		Source: source,
		URL:    item.HTMLURL,
		Kind:   item.Kind,
		Number: item.Number,
		Title:  item.Title,
		Body:   item.Body,
	}
}

func updatedIssueData(source, repo string, prev store.Item, item forge.Issue) message.Data {
	changes := make([]string, 0, 3)
	if prev.State != item.State {
		changes = append(changes, fmt.Sprintf("state: %s → %s", prev.State, item.State))
	}
	if prev.Title != item.Title {
		changes = append(changes, fmt.Sprintf("title: %s → %s", prev.Title, item.Title))
	}
	if prev.Comments != item.Comments {
		changes = append(changes, fmt.Sprintf("comments: %d → %d", prev.Comments, item.Comments))
	}
	if len(changes) == 0 {
		changes = append(changes, "updated")
	}
	return message.Data{
		Repo:    repo,
		Source:  source,
		URL:     item.HTMLURL,
		Kind:    item.Kind,
		Number:  item.Number,
		Title:   item.Title,
		Changes: changes,
	}
}

func pipelineFailedData(source, repo string, pipeline forge.Pipeline) message.Data {
	name := pipeline.Name
	if name == "" {
		name = "pipeline"
	}
	ref := pipeline.Ref
	if ref == "" {
		ref = "unknown ref"
	}
	return message.Data{
		Repo:   repo,
		Source: source,
		URL:    pipeline.URL,
		Name:   name,
		Ref:    ref,
		SHA:    strings.TrimSpace(pipeline.SHA),
		Status: pipeline.Status,
	}
}
