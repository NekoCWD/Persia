package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvAndAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_FORGEJO_TOKEN", "local-token")
	t.Setenv("TEST_MATRIX_TOKEN", "matrix-token")
	t.Setenv("TEST_GITHUB_TOKEN", "github-token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
forgejo:
  base_url: https://git.example.org/
  token: ${TEST_FORGEJO_TOKEN}
matrix:
  homeserver: https://matrix.example.org/
  access_token: ${TEST_MATRIX_TOKEN}
  invite_users:
    - "@neko:matrix.example.org"
topic: persia-managed
targets:
  github:
    type: github
    token: ${TEST_GITHUB_TOKEN}
    namespace: neko
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Forgejo.BaseURL != "https://git.example.org" {
		t.Errorf("base url not trimmed: %q", cfg.Forgejo.BaseURL)
	}
	if cfg.Forgejo.Token != "local-token" {
		t.Errorf("env not expanded: %q", cfg.Forgejo.Token)
	}
	if cfg.Matrix.Homeserver != "https://matrix.example.org" {
		t.Errorf("matrix homeserver not trimmed: %q", cfg.Matrix.Homeserver)
	}
	if cfg.SyncInterval.Std() != 5*time.Minute {
		t.Errorf("unexpected sync interval: %v", cfg.SyncInterval.Std())
	}
	if cfg.Targets["github"].BaseURL != "https://api.github.com" {
		t.Errorf("github base url default missing: %q", cfg.Targets["github"].BaseURL)
	}
	if cfg.Targets["github"].SSHHost != "github.com" {
		t.Errorf("github ssh host default missing: %q", cfg.Targets["github"].SSHHost)
	}
	if !cfg.Targets["github"].Private() {
		t.Error("target should be private by default")
	}
	if cfg.DeployKeyName != "persia {repo}" {
		t.Errorf("unexpected deploy key name default: %q", cfg.DeployKeyName)
	}
	if len(cfg.Forgejo.Owners) != 0 {
		t.Errorf("owners should stay empty without username: %v", cfg.Forgejo.Owners)
	}
}

func TestOwnersDefaultToUsername(t *testing.T) {
	t.Setenv("TEST_FORGEJO_TOKEN", "local-token")
	t.Setenv("TEST_MATRIX_TOKEN", "matrix-token")

	write := func(t *testing.T, owners string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.yaml")
		content := `
forgejo:
  base_url: https://git.example.org
  token: ${TEST_FORGEJO_TOKEN}
  username: alice
` + owners + `
matrix:
  homeserver: https://matrix.example.org
  access_token: ${TEST_MATRIX_TOKEN}
  invite_users:
    - "@neko:matrix.example.org"
`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("fallback to username", func(t *testing.T) {
		cfg, err := Load(write(t, ""))
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if strings.Join(cfg.Forgejo.Owners, ",") != "alice" {
			t.Errorf("unexpected owners: %v", cfg.Forgejo.Owners)
		}
	})

	t.Run("explicit owners", func(t *testing.T) {
		cfg, err := Load(write(t, "  owners:\n    - alice\n    - \" team \"\n"))
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if strings.Join(cfg.Forgejo.Owners, ",") != "alice,team" {
			t.Errorf("unexpected owners: %v", cfg.Forgejo.Owners)
		}
	})
}

func TestDeployKeyNameOverride(t *testing.T) {
	t.Setenv("TEST_FORGEJO_TOKEN", "local-token")
	t.Setenv("TEST_MATRIX_TOKEN", "matrix-token")
	t.Setenv("TEST_GITHUB_TOKEN", "github-token")

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
forgejo:
  base_url: https://git.example.org
  token: ${TEST_FORGEJO_TOKEN}
matrix:
  homeserver: https://matrix.example.org
  access_token: ${TEST_MATRIX_TOKEN}
  invite_users:
    - "@neko:matrix.example.org"
deploy_key_name: "persia {repo}"
targets:
  github:
    type: github
    token: ${TEST_GITHUB_TOKEN}
    deploy_key_name: "{target}: {repo}"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DeployKeyName != "persia {repo}" {
		t.Errorf("unexpected global deploy key name: %q", cfg.DeployKeyName)
	}
	if cfg.Targets["github"].DeployKeyName != "{target}: {repo}" {
		t.Errorf("unexpected target deploy key name: %q", cfg.Targets["github"].DeployKeyName)
	}
}

func TestMirrorSyncOnCommit(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }

	t.Run("default enabled", func(t *testing.T) {
		cfg := &Config{}
		if !cfg.MirrorSyncOnCommitEnabled() {
			t.Error("sync_on_commit should default to true")
		}
	})

	t.Run("global disabled", func(t *testing.T) {
		cfg := &Config{MirrorSyncOnCommit: boolPtr(false)}
		if cfg.MirrorSyncOnCommitEnabled() {
			t.Error("global setting should disable sync_on_commit")
		}
	})

	t.Run("target override", func(t *testing.T) {
		cfg := &Config{MirrorSyncOnCommit: boolPtr(false)}
		target := Target{SyncOnCommit: boolPtr(true)}
		if !target.SyncOnCommitEnabled(cfg) {
			t.Error("target override should enable sync_on_commit")
		}
	})

	t.Run("target inherits global", func(t *testing.T) {
		cfg := &Config{MirrorSyncOnCommit: boolPtr(false)}
		if (Target{}).SyncOnCommitEnabled(cfg) {
			t.Error("target should inherit the global setting")
		}
	})
}

func TestSSHAddress(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   string
	}{
		{
			name:   "scp style",
			target: Target{BaseURL: "https://github.com", SSHHost: "github.com"},
			want:   "git@github.com:alice/demo.git",
		},
		{
			name:   "ssh port",
			target: Target{BaseURL: "https://git.example.org", SSHPort: 2222},
			want:   "ssh://git@git.example.org:2222/alice/demo.git",
		},
		{
			name:   "nested namespace",
			target: Target{BaseURL: "https://gitlab.com/api/v4"},
			want:   "git@gitlab.com:group/sub/demo.git",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.target.SSHAddress("alice", "demo")
			if test.name == "nested namespace" {
				got, err = test.target.SSHAddress("group/sub", "demo")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateRequiresInviteUser(t *testing.T) {
	cfg := &Config{
		Forgejo: ForgejoConfig{BaseURL: "https://git.example.org", Token: "x"},
		Matrix: MatrixConfig{
			Homeserver:  "https://matrix.example.org",
			AccessToken: "x",
		},
		SyncInterval: Duration(5 * time.Minute),
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "invite_users") {
		t.Fatalf("expected invite_users validation error, got %v", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	for _, key := range []string{
		"PERSIA_FORGEJO_TOKEN",
		"PERSIA_MATRIX_TOKEN",
		"PERSIA_GITHUB_TOKEN",
		"PERSIA_GITLAB_TOKEN",
		"PERSIA_ALTLINUX_TOKEN",
	} {
		t.Setenv(key, "test-token")
	}
	t.Setenv("PERSIA_DATABASE", "")

	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("example config is invalid: %v", err)
	}
	if _, ok := cfg.Targets["github"]; !ok {
		t.Fatal("github target missing")
	}
}

func TestParseMirrorAddress(t *testing.T) {
	tests := []struct {
		address string
		host    string
		path    string
		ok      bool
	}{
		{"ssh://git@github.com/acme/widget.git", "github.com", "acme/widget.git", true},
		{"git@gitlab.com:acme/widget.git", "gitlab.com", "acme/widget.git", true},
		{"https://altlinux.space/alice/widget.git", "altlinux.space", "alice/widget.git", true},
		{"ssh://git@git.example.org:2222/group/sub/repo.git", "git.example.org", "group/sub/repo.git", true},
		{"", "", "", false},
		{"not-an-address", "", "", false},
	}
	for _, test := range tests {
		host, path, ok := ParseMirrorAddress(test.address)
		if ok != test.ok {
			t.Errorf("ParseMirrorAddress(%q) ok = %v, want %v", test.address, ok, test.ok)
			continue
		}
		if !test.ok {
			continue
		}
		if host != test.host || path != test.path {
			t.Errorf("ParseMirrorAddress(%q) = (%q, %q), want (%q, %q)",
				test.address, host, path, test.host, test.path)
		}
	}
}

func TestRepoURL(t *testing.T) {
	target := Target{BaseURL: "https://api.github.com", SSHHost: "github.com"}
	tests := []struct {
		name     string
		template string
		address  string
		want     string
	}{
		{
			name:    "derived from ssh address",
			address: "ssh://git@github.com/acme/widget.git",
			want:    "https://github.com/acme/widget",
		},
		{
			name:    "derived from scp address",
			address: "git@gitlab.com:acme/widget.git",
			want:    "https://gitlab.com/acme/widget",
		},
		{
			name:     "custom template",
			template: "https://{host}/{namespace}/{name}",
			address:  "https://altlinux.space/alice/widget.git",
			want:     "https://altlinux.space/alice/widget",
		},
		{
			name:     "template with target and local repo",
			template: "https://{host}/{remote}?src={target}&from={repo}",
			address:  "git@github.com:acme/widget.git",
			want:     "https://github.com/acme/widget?src=github&from=acme/widget",
		},
		{
			name:    "unparsable address falls back to host only",
			address: "garbage",
			want:    "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := target.RepoURL(test.template, "github", "acme", "widget", test.address)
			if got != test.want {
				t.Errorf("RepoURL() = %q, want %q", got, test.want)
			}
		})
	}
}
