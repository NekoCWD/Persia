package forge

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Forgejo is an API client for a Forgejo instance. It is used both for the
// local source-of-truth instance and for remote Forgejo targets.
type Forgejo struct {
	name      string
	api       *apiClient
	namespace string
	private   bool
}

// NewForgejo creates a Forgejo client. baseURL is the instance root, for
// example https://git.example.org; /api/v1 is appended automatically.
func NewForgejo(name, baseURL, token, namespace string, private bool, timeout time.Duration) *Forgejo {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "token " + token
	}
	return &Forgejo{
		name: name,
		api: newAPIClient(
			strings.TrimRight(baseURL, "/")+"/api/v1",
			headers,
			timeout,
		),
		namespace: namespace,
		private:   private,
	}
}

// Name returns the target name.
func (f *Forgejo) Name() string { return f.name }

type forgejoUser struct {
	Login string `json:"login"`
}

type forgejoRepo struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	FullName      string   `json:"full_name"`
	HTMLURL       string   `json:"html_url"`
	Description   string   `json:"description"`
	AvatarURL     string   `json:"avatar_url"`
	Private       bool     `json:"private"`
	Topics        []string `json:"topics"`
	DefaultBranch string   `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (f *Forgejo) ref(r forgejoRepo) Ref {
	return Ref{
		Owner:   r.Owner.Login,
		Name:    r.Name,
		HTMLURL: r.HTMLURL,
		ID:      strconv.FormatInt(r.ID, 10),
	}
}

// Check verifies connectivity and authentication.
func (f *Forgejo) Check(ctx context.Context) error {
	var user forgejoUser
	if err := f.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return err
	}
	if user.Login == "" {
		return fmt.Errorf("forgejo returned an empty user")
	}
	return nil
}

func (f *Forgejo) owner(ctx context.Context) (string, error) {
	if f.namespace != "" {
		return f.namespace, nil
	}
	var user forgejoUser
	if err := f.api.do(ctx, "GET", "/user", nil, nil, &user); err != nil {
		return "", fmt.Errorf("resolve forgejo user: %w", err)
	}
	if user.Login == "" {
		return "", fmt.Errorf("resolve forgejo user: empty login")
	}
	f.namespace = user.Login
	return f.namespace, nil
}

// EnsureRepo creates the remote repository if it does not exist yet.
func (f *Forgejo) EnsureRepo(ctx context.Context, repo Repo) (Ref, error) {
	owner, err := f.owner(ctx)
	if err != nil {
		return Ref{}, err
	}

	repoPath := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo.Name))
	var existing forgejoRepo
	switch err := f.api.do(ctx, "GET", repoPath, nil, nil, &existing); {
	case err == nil:
		if existing.Private != f.private {
			body := map[string]any{"private": f.private}
			_ = f.api.do(ctx, "PATCH", repoPath, nil, body, nil)
		}
		return f.ref(existing), nil
	case err != ErrNotFound:
		return Ref{}, fmt.Errorf("get repository %s/%s: %w", owner, repo.Name, err)
	}

	body := map[string]any{
		"name":      repo.Name,
		"private":   f.private,
		"auto_init": false,
	}

	var created forgejoRepo
	var createErr error
	if f.isOrg(ctx, owner) {
		createErr = f.api.do(ctx, "POST", "/orgs/"+url.PathEscape(owner)+"/repos", nil, body, &created)
	} else {
		createErr = f.api.do(ctx, "POST", "/user/repos", nil, body, &created)
	}
	if createErr != nil {
		// A concurrent run may have created it already.
		if isStatus(createErr, 409) || isStatus(createErr, 422) {
			if err := f.api.do(ctx, "GET", repoPath, nil, nil, &existing); err == nil {
				return f.ref(existing), nil
			}
		}
		return Ref{}, fmt.Errorf("create repository %s/%s: %w", owner, repo.Name, createErr)
	}

	if created.FullName != "" {
		return f.ref(created), nil
	}
	if err := f.api.do(ctx, "GET", repoPath, nil, nil, &existing); err != nil {
		return Ref{}, fmt.Errorf("get created repository %s/%s: %w", owner, repo.Name, err)
	}
	return f.ref(existing), nil
}

func (f *Forgejo) isOrg(ctx context.Context, owner string) bool {
	var org struct {
		Username string `json:"username"`
	}
	return f.api.do(ctx, "GET", "/orgs/"+url.PathEscape(owner), nil, nil, &org) == nil
}

type forgejoKey struct {
	ID       int64  `json:"id"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	ReadOnly bool   `json:"read_only"`
}

// RemoveDeployKey removes the Forgejo deploy key matching the given public key.
func (f *Forgejo) RemoveDeployKey(ctx context.Context, ref Ref, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := fmt.Sprintf("/repos/%s/%s/keys", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var keys []forgejoKey
	if err := f.api.do(ctx, "GET", path, url.Values{"limit": {"100"}}, nil, &keys); err != nil {
		return false, fmt.Errorf("list deploy keys: %w", err)
	}
	want := keyBody(publicKey)
	removed := false
	for _, key := range keys {
		if keyBody(key.Key) != want {
			continue
		}
		deletePath := fmt.Sprintf("/repos/%s/%s/keys/%d",
			url.PathEscape(ref.Owner), url.PathEscape(ref.Name), key.ID)
		if err := f.api.do(ctx, "DELETE", deletePath, nil, nil, nil); err != nil && !isStatus(err, 404) {
			return removed, fmt.Errorf("remove deploy key: %w", err)
		}
		removed = true
	}
	return removed, nil
}

// EnsureDeployKey adds a deploy key unless an equivalent key already exists.
func (f *Forgejo) EnsureDeployKey(ctx context.Context, ref Ref, title, publicKey string) (bool, error) {
	if strings.TrimSpace(publicKey) == "" {
		return false, nil
	}
	path := fmt.Sprintf("/repos/%s/%s/keys", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))

	var keys []forgejoKey
	if err := f.api.do(ctx, "GET", path, url.Values{"limit": {"100"}}, nil, &keys); err != nil {
		return false, fmt.Errorf("list deploy keys: %w", err)
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
	if err := f.api.do(ctx, "POST", path, nil, body, nil); err != nil {
		if isStatus(err, 422) {
			// A key registered as a regular user SSH key cannot be added as
			// a deploy key. The mirror can still push when that user has
			// write access to the repository, so report this as the dedicated
			// sentinel error instead of failing.
			message := strings.ToLower(err.Error())
			if strings.Contains(message, "already") {
				return false, nil
			}
			if strings.Contains(message, "has been used") {
				return false, fmt.Errorf("add deploy key: %w", ErrDeployKeyExistsAsUser)
			}
		}
		return false, fmt.Errorf("add deploy key: %w", err)
	}
	return true, nil
}

type forgejoIssue struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	Comments  int    `json:"comments"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Body        string `json:"body"`
	PullRequest *struct {
		Merged   bool   `json:"merged"`
		MergedAt string `json:"merged_at"`
		HTMLURL  string `json:"html_url"`
	} `json:"pull_request"`
}

// ListIssues returns issues and pull requests from Forgejo.
func (f *Forgejo) ListIssues(ctx context.Context, ref Ref) ([]Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	query := url.Values{
		"state": {"all"},
		"type":  {"all"},
		"sort":  {"recentupdate"},
		"limit": {"100"},
	}

	var raw []forgejoIssue
	if err := f.api.do(ctx, "GET", path, query, nil, &raw); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list issues: %w", err)
	}

	issues := make([]Issue, 0, len(raw))
	for _, item := range raw {
		kind := "issue"
		state := item.State
		htmlURL := item.HTMLURL
		if item.PullRequest != nil {
			kind = "pull_request"
			if item.PullRequest.Merged || item.PullRequest.MergedAt != "" {
				state = "merged"
			}
			if item.PullRequest.HTMLURL != "" {
				htmlURL = item.PullRequest.HTMLURL
			}
		}
		issues = append(issues, Issue{
			Kind:      kind,
			Number:    int(item.Number),
			Title:     item.Title,
			State:     state,
			HTMLURL:   htmlURL,
			Author:    item.User.Login,
			Comments:  item.Comments,
			UpdatedAt: parseTime(item.UpdatedAt),
			Body:      item.Body,
		})
	}
	return issues, nil
}

type forgejoRun struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	HTMLURL    string `json:"html_url"`
	PrettyRef  string `json:"prettyref"`
	CommitSHA  string `json:"commit_sha"`
	WorkflowID string `json:"workflow_id"`
	UpdatedAt  string `json:"updated"`
}

// ListPipelines returns Forgejo Actions runs.
func (f *Forgejo) ListPipelines(ctx context.Context, ref Ref) ([]Pipeline, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var response struct {
		WorkflowRuns []forgejoRun `json:"workflow_runs"`
	}
	if err := f.api.do(ctx, "GET", path, url.Values{"limit": {"50"}}, nil, &response); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list actions runs: %w", err)
	}

	runs := make([]Pipeline, 0, len(response.WorkflowRuns))
	for _, run := range response.WorkflowRuns {
		name := run.Title
		if name == "" {
			name = run.WorkflowID
		}
		runs = append(runs, Pipeline{
			ID:        strconv.FormatInt(run.ID, 10),
			Name:      name,
			Status:    strings.ToLower(run.Status),
			URL:       run.HTMLURL,
			Ref:       run.PrettyRef,
			SHA:       run.CommitSHA,
			UpdatedAt: parseTime(run.UpdatedAt),
		})
	}
	return runs, nil
}

// ListManagedRepos returns repositories on the local instance that carry the
// given topic. When owners is not empty, only repositories owned by one of
// those users or organizations are returned (case-insensitive). The special
// value "*" disables the owner filter.
func (f *Forgejo) ListManagedRepos(ctx context.Context, topic string, owners []string) ([]Repo, error) {
	const pageSize = 50
	seen := map[string]bool{}
	allowed := make(map[string]struct{}, len(owners))
	unrestricted := false
	for _, owner := range owners {
		owner = strings.TrimSpace(owner)
		switch {
		case owner == "":
		case owner == "*":
			unrestricted = true
		default:
			allowed[strings.ToLower(owner)] = struct{}{}
		}
	}
	if unrestricted {
		allowed = nil
	}
	var repos []Repo

	for page := 1; page <= 200; page++ {
		query := url.Values{
			"q":       {topic},
			"topic":   {"true"},
			"private": {"true"},
			"limit":   {strconv.Itoa(pageSize)},
			"page":    {strconv.Itoa(page)},
		}
		var response struct {
			Data []forgejoRepo `json:"data"`
		}
		if err := f.api.do(ctx, "GET", "/repos/search", query, nil, &response); err != nil {
			return nil, fmt.Errorf("search repositories: %w", err)
		}
		for _, item := range response.Data {
			if !hasTopic(item.Topics, topic) || seen[item.FullName] {
				continue
			}
			if len(allowed) > 0 {
				if _, ok := allowed[strings.ToLower(item.Owner.Login)]; !ok {
					continue
				}
			}
			seen[item.FullName] = true
			repos = append(repos, Repo{
				Owner:         item.Owner.Login,
				Name:          item.Name,
				FullName:      item.FullName,
				HTMLURL:       item.HTMLURL,
				Description:   strings.TrimSpace(item.Description),
				AvatarURL:     strings.TrimSpace(item.AvatarURL),
				Private:       item.Private,
				Topics:        item.Topics,
				DefaultBranch: item.DefaultBranch,
			})
		}
		if len(response.Data) < pageSize {
			break
		}
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	return repos, nil
}

// PushMirror is a Forgejo push mirror.
type PushMirror struct {
	RemoteName    string `json:"remote_name"`
	RemoteAddress string `json:"remote_address"`
	PublicKey     string `json:"public_key"`
	LastError     string `json:"last_error"`
	SyncOnCommit  bool   `json:"sync_on_commit"`
	Interval      string `json:"interval"`
	LastUpdate    string `json:"last_update"`
}

// ListPushMirrors returns all push mirrors of a repository.
func (f *Forgejo) ListPushMirrors(ctx context.Context, owner, name string) ([]PushMirror, error) {
	path := fmt.Sprintf("/repos/%s/%s/push_mirrors", url.PathEscape(owner), url.PathEscape(name))
	var mirrors []PushMirror
	if err := f.api.do(ctx, "GET", path, nil, nil, &mirrors); err != nil {
		return nil, fmt.Errorf("list push mirrors: %w", err)
	}
	return mirrors, nil
}

// CreatePushMirror adds a push mirror and returns the generated key. SSH keys
// are generated by Forgejo; HTTPS mirrors authenticate with username/password.
func (f *Forgejo) CreatePushMirror(
	ctx context.Context,
	owner, name, address, interval string,
	syncOnCommit, useSSH bool,
	username, password string,
) (PushMirror, error) {
	body := map[string]any{
		"remote_address": address,
		"sync_on_commit": syncOnCommit,
		"interval":       interval,
	}
	if useSSH {
		body["use_ssh"] = true
	} else {
		body["use_ssh"] = false
		body["remote_username"] = username
		body["remote_password"] = password
	}
	path := fmt.Sprintf("/repos/%s/%s/push_mirrors", url.PathEscape(owner), url.PathEscape(name))
	var mirror PushMirror
	if err := f.api.do(ctx, "POST", path, nil, body, &mirror); err != nil {
		return PushMirror{}, fmt.Errorf("create push mirror: %w", err)
	}
	return mirror, nil
}

// DeletePushMirror removes a push mirror by its remote name.
func (f *Forgejo) DeletePushMirror(ctx context.Context, owner, name, remoteName string) error {
	path := fmt.Sprintf("/repos/%s/%s/push_mirrors/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(remoteName))
	if err := f.api.do(ctx, "DELETE", path, nil, nil, nil); err != nil {
		return fmt.Errorf("delete push mirror %q: %w", remoteName, err)
	}
	return nil
}

// TriggerPushMirror asks Forgejo to synchronize all push mirrors now.
func (f *Forgejo) TriggerPushMirror(ctx context.Context, owner, name string) error {
	path := fmt.Sprintf("/repos/%s/%s/push_mirrors-sync", url.PathEscape(owner), url.PathEscape(name))
	if err := f.api.do(ctx, "POST", path, nil, nil, nil); err != nil {
		return fmt.Errorf("trigger push mirror: %w", err)
	}
	return nil
}

func hasTopic(topics []string, topic string) bool {
	for _, item := range topics {
		if strings.EqualFold(item, topic) {
			return true
		}
	}
	return false
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// UpdateRepoDescription updates the Forgejo repository description. Empty
// descriptions are still sent so stale text can be cleared.
func (f *Forgejo) UpdateRepoDescription(ctx context.Context, ref Ref, description string) error {
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(ref.Owner), url.PathEscape(ref.Name))
	var existing forgejoRepo
	if err := f.api.do(ctx, "GET", path, nil, nil, &existing); err == nil {
		if existing.Description == description {
			return nil
		}
	}
	body := map[string]any{"description": description}
	return f.api.do(ctx, "PATCH", path, nil, body, nil)
}
