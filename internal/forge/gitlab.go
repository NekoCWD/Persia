package forge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitLab is a GitLab API adapter.
type GitLab struct {
	name      string
	api       *apiClient
	namespace string
	private   bool
	username  string
	nsID      int64
}

// NewGitLab creates a GitLab adapter.
func NewGitLab(name, baseURL, token, namespace string, private bool, timeout time.Duration) *GitLab {
	if baseURL == "" {
		baseURL = "https://gitlab.com/api/v4"
	}
	return &GitLab{
		name: name,
		api: newAPIClient(baseURL, map[string]string{
			"PRIVATE-TOKEN": token,
		}, timeout),
		namespace: namespace,
		private:   private,
	}
}

// Name returns the target name.
func (g *GitLab) Name() string { return g.name }

type gitlabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type gitlabProject struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	Description       string `json:"description"`
	Visibility        string `json:"visibility"`
	Owner             struct {
		Username string `json:"username"`
	} `json:"owner"`
	Namespace struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

func (g *GitLab) ref(project gitlabProject) Ref {
	owner := project.Namespace.FullPath
	if owner == "" {
		owner = g.namespace
	}
	name := project.Path
	if name == "" {
		name = project.Name
	}
	return Ref{
		Owner:   owner,
		Name:    name,
		HTMLURL: project.WebURL,
		ID:      strconv.FormatInt(project.ID, 10),
	}
}

// Check verifies connectivity and authentication.
func (g *GitLab) Check(ctx context.Context) error {
	var user gitlabUser
	if err := g.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return err
	}
	if user.Username == "" {
		return fmt.Errorf("gitlab returned an empty user")
	}
	g.username = user.Username
	return nil
}

func (g *GitLab) currentUser(ctx context.Context) (string, error) {
	if g.username != "" {
		return g.username, nil
	}
	var user gitlabUser
	if err := g.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return "", fmt.Errorf("resolve gitlab user: %w", err)
	}
	if user.Username == "" {
		return "", fmt.Errorf("resolve gitlab user: empty username")
	}
	g.username = user.Username
	return g.username, nil
}

func (g *GitLab) namespaceID(ctx context.Context) (int64, error) {
	if g.nsID != 0 {
		return g.nsID, nil
	}
	namespace := g.namespace
	if namespace == "" {
		username, err := g.currentUser(ctx)
		if err != nil {
			return 0, err
		}
		namespace = username
	}

	var namespaces []struct {
		ID       int64  `json:"id"`
		Path     string `json:"path"`
		FullPath string `json:"full_path"`
		Kind     string `json:"kind"`
	}
	query := url.Values{"search": {namespace}, "per_page": {"100"}}
	if err := g.api.do(ctx, "GET", "/namespaces", query, nil, &namespaces); err != nil {
		return 0, fmt.Errorf("resolve gitlab namespace %q: %w", namespace, err)
	}
	for _, item := range namespaces {
		if strings.EqualFold(item.FullPath, namespace) || strings.EqualFold(item.Path, namespace) {
			g.nsID = item.ID
			g.namespace = item.FullPath
			return g.nsID, nil
		}
	}
	return 0, fmt.Errorf("gitlab namespace %q not found", namespace)
}

func gitlabSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-.")
	if slug == "" {
		return "repo"
	}
	return slug
}

func (g *GitLab) projectPath(ref Ref) string {
	id := ref.ID
	if id == "" {
		id = url.PathEscape(ref.Owner + "/" + ref.Name)
	}
	return "/projects/" + id
}

// EnsureRepo creates the remote GitLab project if needed.
func (g *GitLab) EnsureRepo(ctx context.Context, repo Repo) (Ref, error) {
	namespace := g.namespace
	if namespace == "" {
		username, err := g.currentUser(ctx)
		if err != nil {
			return Ref{}, err
		}
		namespace = username
	}
	projectPath := gitlabSlug(repo.Name)
	fullPath := namespace + "/" + projectPath
	path := "/projects/" + url.PathEscape(fullPath)

	var existing gitlabProject
	switch err := g.api.do(ctx, "GET", path, nil, nil, &existing); {
	case err == nil:
		desired := "public"
		if g.private {
			desired = "private"
		}
		if existing.Visibility != "" && existing.Visibility != desired {
			body := map[string]any{"visibility": desired}
			_ = g.api.do(ctx, "PUT", path, nil, body, nil)
		}
		return g.ref(existing), nil
	case err != ErrNotFound:
		return Ref{}, fmt.Errorf("get gitlab project %s: %w", fullPath, err)
	}

	namespaceID, err := g.namespaceID(ctx)
	if err != nil {
		return Ref{}, err
	}
	visibility := "private"
	if !g.private {
		visibility = "public"
	}
	body := map[string]any{
		"name":                   repo.Name,
		"path":                   projectPath,
		"namespace_id":           namespaceID,
		"visibility":             visibility,
		"initialize_with_readme": false,
	}

	var created gitlabProject
	if err := g.api.do(ctx, "POST", "/projects", nil, body, &created); err != nil {
		if isStatus(err, 400) && strings.Contains(strings.ToLower(err.Error()), "already") {
			if err := g.api.do(ctx, "GET", path, nil, nil, &existing); err == nil {
				return g.ref(existing), nil
			}
		}
		return Ref{}, fmt.Errorf("create gitlab project %s: %w", fullPath, err)
	}
	if created.ID != 0 {
		return g.ref(created), nil
	}
	if err := g.api.do(ctx, "GET", path, nil, nil, &existing); err != nil {
		return Ref{}, fmt.Errorf("get created gitlab project %s: %w", fullPath, err)
	}
	return g.ref(existing), nil
}

type gitlabKey struct {
	ID  int64  `json:"id"`
	Key string `json:"key"`
}

// RemoveDeployKey removes the GitLab deploy key matching the given public key.
func (g *GitLab) RemoveDeployKey(ctx context.Context, ref Ref, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := g.projectPath(ref) + "/deploy_keys"
	var keys []gitlabKey
	if err := g.api.do(ctx, "GET", path, nil, nil, &keys); err != nil {
		return false, fmt.Errorf("list gitlab deploy keys: %w", err)
	}
	want := keyBody(publicKey)
	removed := false
	for _, key := range keys {
		if keyBody(key.Key) != want {
			continue
		}
		deletePath := g.projectPath(ref) + "/deploy_keys/" + strconv.FormatInt(key.ID, 10)
		if err := g.api.do(ctx, "DELETE", deletePath, nil, nil, nil); err != nil && !isStatus(err, 404) {
			return removed, fmt.Errorf("remove gitlab deploy key: %w", err)
		}
		removed = true
	}
	return removed, nil
}

// EnsureDeployKey adds a deploy key unless it already exists.
func (g *GitLab) EnsureDeployKey(ctx context.Context, ref Ref, title, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := g.projectPath(ref) + "/deploy_keys"
	var keys []gitlabKey
	if err := g.api.do(ctx, "GET", path, nil, nil, &keys); err != nil {
		return false, fmt.Errorf("list gitlab deploy keys: %w", err)
	}
	want := keyBody(publicKey)
	for _, key := range keys {
		if keyBody(key.Key) == want {
			return false, nil
		}
	}

	body := map[string]any{
		"title":    title,
		"key":      strings.TrimSpace(publicKey),
		"can_push": true,
	}
	if err := g.api.do(ctx, "POST", path, nil, body, nil); err != nil {
		if isStatus(err, 400) && strings.Contains(strings.ToLower(err.Error()), "already") {
			return false, nil
		}
		return false, fmt.Errorf("add gitlab deploy key: %w", err)
	}
	return true, nil
}

type gitlabIssue struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	UserNotes   int    `json:"user_notes_count"`
	UpdatedAt   string `json:"updated_at"`
	MergedAt    string `json:"merged_at"`
	Author      struct {
		Username string `json:"username"`
	} `json:"author"`
}

// ListIssues returns GitLab issues and merge requests.
func (g *GitLab) ListIssues(ctx context.Context, ref Ref) ([]Issue, error) {
	query := url.Values{
		"state":    {"all"},
		"order_by": {"updated_at"},
		"sort":     {"desc"},
		"per_page": {"100"},
	}

	var rawIssues []gitlabIssue
	if err := g.api.do(ctx, "GET", g.projectPath(ref)+"/issues", query, nil, &rawIssues); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list gitlab issues: %w", err)
	}

	var rawMergeRequests []gitlabIssue
	if err := g.api.do(ctx, "GET", g.projectPath(ref)+"/merge_requests", query, nil, &rawMergeRequests); err != nil {
		if err != ErrNotFound {
			return nil, fmt.Errorf("list gitlab merge requests: %w", err)
		}
	}

	issues := make([]Issue, 0, len(rawIssues)+len(rawMergeRequests))
	for _, item := range rawIssues {
		issues = append(issues, Issue{
			Kind:      "issue",
			Number:    item.IID,
			Title:     item.Title,
			Body:      item.Description,
			State:     item.State,
			HTMLURL:   item.WebURL,
			Author:    item.Author.Username,
			Comments:  item.UserNotes,
			UpdatedAt: parseTime(item.UpdatedAt),
		})
	}
	for _, item := range rawMergeRequests {
		state := item.State
		if item.MergedAt != "" {
			state = "merged"
		}
		issues = append(issues, Issue{
			Kind:      "pull_request",
			Number:    item.IID,
			Title:     item.Title,
			Body:      item.Description,
			State:     state,
			HTMLURL:   item.WebURL,
			Author:    item.Author.Username,
			Comments:  item.UserNotes,
			UpdatedAt: parseTime(item.UpdatedAt),
		})
	}
	return issues, nil
}

type gitlabPipeline struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	WebURL    string `json:"web_url"`
	UpdatedAt string `json:"updated_at"`
}

// ListPipelines returns GitLab CI pipelines.
func (g *GitLab) ListPipelines(ctx context.Context, ref Ref) ([]Pipeline, error) {
	path := g.projectPath(ref) + "/pipelines"
	query := url.Values{
		"order_by": {"updated_at"},
		"sort":     {"desc"},
		"per_page": {"50"},
	}
	var raw []gitlabPipeline
	if err := g.api.do(ctx, "GET", path, query, nil, &raw); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list gitlab pipelines: %w", err)
	}

	pipelines := make([]Pipeline, 0, len(raw))
	for _, item := range raw {
		pipelines = append(pipelines, Pipeline{
			ID:        strconv.FormatInt(item.ID, 10),
			Name:      "pipeline",
			Status:    strings.ToLower(item.Status),
			URL:       item.WebURL,
			Ref:       item.Ref,
			SHA:       item.SHA,
			UpdatedAt: parseTime(item.UpdatedAt),
		})
	}
	return pipelines, nil
}

// UpdateRepoDescription updates the GitLab project description.
func (g *GitLab) UpdateRepoDescription(ctx context.Context, ref Ref, description string) error {
	id := ref.ID
	if id == "" {
		id = url.PathEscape(ref.Owner + "/" + ref.Name)
	}
	path := "/projects/" + id
	var existing gitlabProject
	if err := g.api.do(ctx, "GET", path, nil, nil, &existing); err == nil {
		if existing.Description == description {
			return nil
		}
	}
	body := map[string]any{"description": description}
	return g.api.do(ctx, "PUT", path, nil, body, nil)
}
