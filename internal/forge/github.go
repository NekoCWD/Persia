package forge

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub is a GitHub API adapter.
type GitHub struct {
	name      string
	api       *apiClient
	namespace string
	private   bool
	login     string
}

// NewGitHub creates a GitHub adapter.
func NewGitHub(name, baseURL, token, namespace string, private bool, timeout time.Duration) *GitHub {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &GitHub{
		name: name,
		api: newAPIClient(baseURL, map[string]string{
			"Authorization":        "Bearer " + token,
			"X-GitHub-Api-Version": "2022-11-28",
		}, timeout),
		namespace: namespace,
		private:   private,
	}
}

// Name returns the target name.
func (g *GitHub) Name() string { return g.name }

type githubUser struct {
	Login string `json:"login"`
}

type githubRepo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	HTMLURL     string `json:"html_url"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (g *GitHub) ref(repo githubRepo) Ref {
	return Ref{
		Owner:   repo.Owner.Login,
		Name:    repo.Name,
		HTMLURL: repo.HTMLURL,
		ID:      strconv.FormatInt(repo.ID, 10),
	}
}

// Check verifies connectivity and authentication.
func (g *GitHub) Check(ctx context.Context) error {
	var user githubUser
	if err := g.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return err
	}
	if user.Login == "" {
		return fmt.Errorf("github returned an empty user")
	}
	g.login = user.Login
	return nil
}

func (g *GitHub) currentLogin(ctx context.Context) (string, error) {
	if g.login != "" {
		return g.login, nil
	}
	var user githubUser
	if err := g.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return "", fmt.Errorf("resolve github user: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("resolve github user: empty login")
	}
	g.login = user.Login
	return g.login, nil
}

func (g *GitHub) owner(ctx context.Context) (string, bool, error) {
	login, err := g.currentLogin(ctx)
	if err != nil {
		return "", false, err
	}
	namespace := g.namespace
	if namespace == "" {
		namespace = login
	}
	if strings.EqualFold(namespace, login) {
		return namespace, false, nil
	}
	var org struct {
		Login string `json:"login"`
	}
	if err := g.api.do(ctx, "GET", "/orgs/"+url.PathEscape(namespace), nil, nil, &org); err != nil {
		if err == ErrNotFound {
			return "", false, fmt.Errorf("namespace %q is neither the authenticated user nor a GitHub organization", namespace)
		}
		return "", false, fmt.Errorf("check github organization %q: %w", namespace, err)
	}
	return namespace, true, nil
}

// EnsureRepo creates the remote repository if needed.
func (g *GitHub) EnsureRepo(ctx context.Context, repo Repo) (Ref, error) {
	owner, isOrg, err := g.owner(ctx)
	if err != nil {
		return Ref{}, err
	}

	repoPath := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo.Name))
	var existing githubRepo
	switch err := g.api.do(ctx, "GET", repoPath, nil, nil, &existing); {
	case err == nil:
		if existing.Private != g.private {
			body := map[string]any{"private": g.private}
			_ = g.api.do(ctx, "PATCH", repoPath, nil, body, nil)
		}
		return g.ref(existing), nil
	case err != ErrNotFound:
		return Ref{}, fmt.Errorf("get github repository %s/%s: %w", owner, repo.Name, err)
	}

	body := map[string]any{
		"name":      repo.Name,
		"private":   g.private,
		"auto_init": false,
	}
	createPath := "/user/repos"
	if isOrg {
		createPath = "/orgs/" + url.PathEscape(owner) + "/repos"
	}

	var created githubRepo
	if err := g.api.do(ctx, "POST", createPath, nil, body, &created); err != nil {
		if isStatus(err, 422) {
			if err := g.api.do(ctx, "GET", repoPath, nil, nil, &existing); err == nil {
				return g.ref(existing), nil
			}
		}
		return Ref{}, fmt.Errorf("create github repository %s/%s: %w", owner, repo.Name, err)
	}
	if created.FullName != "" {
		return g.ref(created), nil
	}
	if err := g.api.do(ctx, "GET", repoPath, nil, nil, &existing); err != nil {
		return Ref{}, fmt.Errorf("get created github repository: %w", err)
	}
	return g.ref(existing), nil
}

type githubKey struct {
	ID  int64  `json:"id"`
	Key string `json:"key"`
}

// RemoveDeployKey removes the GitHub deploy key matching the given public key.
func (g *GitHub) RemoveDeployKey(ctx context.Context, ref Ref, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := fmt.Sprintf("/repos/%s/%s/keys", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var keys []githubKey
	if err := g.api.do(ctx, "GET", path, url.Values{"per_page": {"100"}}, nil, &keys); err != nil {
		return false, fmt.Errorf("list github deploy keys: %w", err)
	}
	want := keyBody(publicKey)
	removed := false
	for _, key := range keys {
		if keyBody(key.Key) != want {
			continue
		}
		if err := g.api.do(ctx, "DELETE", fmt.Sprintf("%s/%d", path, key.ID), nil, nil, nil); err != nil && !isStatus(err, 404) {
			return removed, fmt.Errorf("remove github deploy key: %w", err)
		}
		removed = true
	}
	return removed, nil
}
func (g *GitHub) EnsureDeployKey(ctx context.Context, ref Ref, title, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := fmt.Sprintf("/repos/%s/%s/keys", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var keys []githubKey
	if err := g.api.do(ctx, "GET", path, url.Values{"per_page": {"100"}}, nil, &keys); err != nil {
		return false, fmt.Errorf("list github deploy keys: %w", err)
	}
	want := keyBody(publicKey)
	for _, key := range keys {
		if keyBody(key.Key) == want {
			return false, nil
		}
	}

	body := map[string]any{
		"title":     title,
		"key":       strings.TrimSpace(publicKey),
		"read_only": false,
	}
	if err := g.api.do(ctx, "POST", path, nil, body, nil); err != nil {
		if isStatus(err, 422) {
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "already") {
				// GitHub rejects deploy keys whose public key is already
				// registered as a user SSH key. The mirror can still push
				// when that user has write access, so treat this as the same
				// usable condition as on altlinux.space.
				return false, ErrDeployKeyExistsAsUser
			}
		}
		return false, fmt.Errorf("add github deploy key: %w", err)
	}
	return true, nil
}

type githubIssue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	Comments  int    `json:"comments"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest *struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
}

// ListIssues returns GitHub issues and pull requests.
func (g *GitHub) ListIssues(ctx context.Context, ref Ref) ([]Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	query := url.Values{
		"state":     {"all"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {"100"},
	}

	var raw []githubIssue
	if err := g.api.do(ctx, "GET", path, query, nil, &raw); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list github issues: %w", err)
	}

	issues := make([]Issue, 0, len(raw))
	for _, item := range raw {
		kind := "issue"
		state := item.State
		if item.PullRequest != nil {
			kind = "pull_request"
			if item.PullRequest.MergedAt != nil && *item.PullRequest.MergedAt != "" {
				state = "merged"
			}
		}
		issues = append(issues, Issue{
			Kind:      kind,
			Number:    item.Number,
			Title:     item.Title,
			Body:      item.Body,
			State:     state,
			HTMLURL:   item.HTMLURL,
			Author:    item.User.Login,
			Comments:  item.Comments,
			UpdatedAt: parseTime(item.UpdatedAt),
		})
	}
	return issues, nil
}

type githubRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	UpdatedAt  string `json:"updated_at"`
}

// ListPipelines returns GitHub Actions workflow runs.
func (g *GitHub) ListPipelines(ctx context.Context, ref Ref) ([]Pipeline, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var response struct {
		WorkflowRuns []githubRun `json:"workflow_runs"`
	}
	if err := g.api.do(ctx, "GET", path, url.Values{"per_page": {"50"}}, nil, &response); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list github workflow runs: %w", err)
	}

	runs := make([]Pipeline, 0, len(response.WorkflowRuns))
	for _, run := range response.WorkflowRuns {
		status := run.Status
		if run.Conclusion != "" {
			status = run.Conclusion
		}
		runs = append(runs, Pipeline{
			ID:        strconv.FormatInt(run.ID, 10),
			Name:      run.Name,
			Status:    strings.ToLower(status),
			URL:       run.HTMLURL,
			Ref:       run.HeadBranch,
			SHA:       run.HeadSHA,
			UpdatedAt: parseTime(run.UpdatedAt),
		})
	}
	return runs, nil
}

// UpdateRepoDescription updates the GitHub repository description.
func (g *GitHub) UpdateRepoDescription(ctx context.Context, ref Ref, description string) error {
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var existing githubRepo
	if err := g.api.do(ctx, "GET", path, nil, nil, &existing); err == nil {
		if existing.Description == description {
			return nil
		}
	}
	body := map[string]any{"description": description}
	return g.api.do(ctx, "PATCH", path, nil, body, nil)
}
