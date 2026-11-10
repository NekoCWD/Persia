package engine

import (
	"strings"
	"testing"

	"git.nekocwd.link/neko/persia/internal/config"
	"git.nekocwd.link/neko/persia/internal/forge"
)

func testEngine(defaultTargets []string) *Engine {
	cfg := &config.Config{
		Topic:             "persia-managed",
		TargetTopicPrefix: "persia-target-",
		NoMirrorTopic:     "persia-no-mirror",
		DefaultTargets:    defaultTargets,
	}
	targets := map[string]remoteTarget{
		"github":   {config: config.Target{Type: "github"}},
		"gitlab":   {config: config.Target{Type: "gitlab"}},
		"altlinux": {config: config.Target{Type: "forgejo"}},
	}
	return &Engine{cfg: cfg, targets: targets}
}

func TestTargetsFor(t *testing.T) {
	tests := []struct {
		name     string
		engine   *Engine
		topics   []string
		expected []string
	}{
		{
			name:     "all targets by default",
			engine:   testEngine(nil),
			topics:   []string{"persia-managed"},
			expected: []string{"altlinux", "github", "gitlab"},
		},
		{
			name:     "default targets",
			engine:   testEngine([]string{"github", "gitlab"}),
			topics:   []string{"persia-managed"},
			expected: []string{"github", "gitlab"},
		},
		{
			name:     "explicit target",
			engine:   testEngine([]string{"github"}),
			topics:   []string{"persia-managed", "persia-target-gitlab"},
			expected: []string{"gitlab"},
		},
		{
			name:     "unknown explicit target disables mirrors",
			engine:   testEngine(nil),
			topics:   []string{"persia-managed", "persia-target-unknown"},
			expected: nil,
		},
		{
			name:     "no mirror topic",
			engine:   testEngine(nil),
			topics:   []string{"persia-managed", "persia-target-github", "persia-no-mirror"},
			expected: nil,
		},
		{
			name:     "case insensitive",
			engine:   testEngine(nil),
			topics:   []string{"Persia-Managed", "Persia-Target-GitHub"},
			expected: []string{"github"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := forge.Repo{Topics: test.topics}
			got := test.engine.targetsFor(repo)
			if strings.Join(got, ",") != strings.Join(test.expected, ",") {
				t.Errorf("got %v, want %v", got, test.expected)
			}
		})
	}
}

func TestNormalizeRemote(t *testing.T) {
	tests := map[string]string{
		"git@github.com:alice/persia.git":       "github.com/alice/persia",
		"ssh://git@github.com/alice/persia.git": "github.com/alice/persia",
		"https://github.com/alice/persia.git":   "github.com/alice/persia",
		"https://github.com/alice/persia/":      "github.com/alice/persia",
	}
	for input, want := range tests {
		if got := normalizeRemote(input); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDeployKeyTitle(t *testing.T) {
	repo := forge.Repo{Owner: "acme", Name: "widget", FullName: "acme/widget"}
	ref := forge.Ref{Owner: "remote-ns", Name: "widget"}

	tests := []struct {
		name     string
		template string
		want     string
	}{
		{
			name:     "default",
			template: "",
			want:     "persia acme/widget",
		},
		{
			name:     "literal",
			template: "persia mirror",
			want:     "persia mirror",
		},
		{
			name:     "placeholders",
			template: "{target}: {repo} -> {remote} ({namespace})",
			want:     "github: acme/widget -> remote-ns/widget (remote-ns)",
		},
		{
			name:     "owner and name",
			template: "{owner}-{name}",
			want:     "acme-widget",
		},
		{
			name:     "blank after expansion falls back",
			template: "   ",
			want:     "persia acme/widget",
		},
		{
			name:     "truncated",
			template: strings.Repeat("x", 100),
			want:     strings.Repeat("x", 80),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deployKeyTitle(test.template, "github", repo, ref); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestIsFailure(t *testing.T) {
	enabled := true
	cfg := &config.Config{NotifyCancelled: &enabled}
	engine := &Engine{cfg: cfg}

	for _, status := range []string{"failure", "failed", "timed_out", "cancelled"} {
		if !engine.isFailure(status) {
			t.Errorf("%q should be a failure", status)
		}
	}
	for _, status := range []string{"success", "running", "pending", "skipped"} {
		if engine.isFailure(status) {
			t.Errorf("%q should not be a failure", status)
		}
	}
}

func TestRepoMetaCache(t *testing.T) {
	engine := &Engine{}

	engine.setRepoMeta([]forge.Repo{
		{FullName: "acme/widget", Description: "Example widget project"},
		{FullName: "acme/demo", Description: "Demo repository",
			AvatarURL: "https://git.example.org/avatar/abc"},
		{FullName: "acme/bare"},
	})

	got := engine.Metadata()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(got), got)
	}
	if got["acme/widget"].Description != "Example widget project" {
		t.Errorf("unexpected description: %q", got["acme/widget"].Description)
	}
	if got["acme/widget"].AvatarURL != "" {
		t.Errorf("repositories without an avatar must stay unpopulated, got %q",
			got["acme/widget"].AvatarURL)
	}
	if got["acme/demo"].AvatarURL != "https://git.example.org/avatar/abc" {
		t.Errorf("unexpected avatar: %q", got["acme/demo"].AvatarURL)
	}
	if _, ok := got["acme/bare"]; ok {
		t.Error("repositories without any metadata should be omitted")
	}

	// The snapshot must not alias internal state.
	got["acme/widget"] = RepoMeta{Description: "mutated"}
	if engine.Metadata()["acme/widget"].Description != "Example widget project" {
		t.Error("Metadata returned an aliased map")
	}

	// A later sync replaces the cache entirely.
	engine.setRepoMeta([]forge.Repo{{FullName: "acme/demo", Description: "Renamed"}})
	got = engine.Metadata()
	if len(got) != 1 || got["acme/demo"].Description != "Renamed" {
		t.Errorf("cache was not replaced: %v", got)
	}
}

func TestTargetTemplateData(t *testing.T) {
	engine := testEngine(nil)
	engine.cfg.Targets = map[string]config.Target{
		"github": {TemplateData: map[string]string{
			"label": "GitHub",
			"docs":  "https://docs.example.org/github",
		}},
		"gitlab": {},
	}

	got := engine.targetTemplateData("github")
	if len(got) != 2 || got["label"] != "GitHub" {
		t.Fatalf("unexpected data: %v", got)
	}
	// The returned map must be a copy so templates cannot mutate config.
	got["label"] = "mutated"
	if engine.cfg.Targets["github"].TemplateData["label"] != "GitHub" {
		t.Error("targetTemplateData returned an aliased map")
	}

	if engine.targetTemplateData("gitlab") != nil {
		t.Error("targets without template_data should yield nil")
	}
	if engine.targetTemplateData("local") != nil {
		t.Error("unknown targets should yield nil")
	}
}
