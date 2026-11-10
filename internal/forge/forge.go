// Package forge contains the remote forge adapters used by Persia.
package forge

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a remote object does not exist.
var ErrNotFound = errors.New("not found")

// ErrDeployKeyExistsAsUser is returned when a public key can't be registered
// as a deploy key because it is already in use as a regular user SSH key.
var ErrDeployKeyExistsAsUser = errors.New("deploy key is already registered as a user SSH key")

// Repo is a repository discovered on the local Forgejo instance.
type Repo struct {
	Owner         string
	Name          string
	FullName      string
	HTMLURL       string
	Description   string
	AvatarURL     string
	Private       bool
	Topics        []string
	DefaultBranch string
}

// Ref identifies a repository on a specific forge.
type Ref struct {
	// Owner is the namespace path (user, organization or GitLab group).
	Owner   string
	Name    string
	HTMLURL string
	// ID is an optional forge-specific identifier (GitLab project ID).
	ID string
}

// Key returns a stable namespace/name identifier.
func (r Ref) Key() string { return r.Owner + "/" + r.Name }

// Issue is a unified issue or pull request representation.
type Issue struct {
	// Kind is either "issue" or "pull_request".
	Kind      string
	Number    int
	Title     string
	Body      string
	State     string
	HTMLURL   string
	Author    string
	Comments  int
	UpdatedAt time.Time
}

// Pipeline is a unified CI run representation.
type Pipeline struct {
	ID        string
	Name      string
	Status    string
	URL       string
	Ref       string
	SHA       string
	UpdatedAt time.Time
}

// Remote is a forge that Persia can create repositories on, add deploy keys
// to and read issues and CI runs from.
type Remote interface {
	// Name returns the configured target name.
	Name() string
	// Check verifies connectivity and authentication.
	Check(ctx context.Context) error
	// EnsureRepo makes sure the repository exists and returns its reference.
	EnsureRepo(ctx context.Context, repo Repo) (Ref, error)
	// UpdateRepoDescription sets the remote repository description, creating
	// or replacing it as supported by the forge.
	UpdateRepoDescription(ctx context.Context, ref Ref, description string) error
	// EnsureDeployKey adds the public key to the repository unless an
	// equivalent key already exists. It reports whether a key was added.
	EnsureDeployKey(ctx context.Context, ref Ref, title, publicKey string) (bool, error)
	// RemoveDeployKey removes a deploy key with the given public key from the
	// repository, if present.
	RemoveDeployKey(ctx context.Context, ref Ref, publicKey string) (bool, error)
	// ListIssues returns recent issues and pull requests.
	ListIssues(ctx context.Context, ref Ref) ([]Issue, error)
	// ListPipelines returns recent CI runs.
	ListPipelines(ctx context.Context, ref Ref) ([]Pipeline, error)
}
