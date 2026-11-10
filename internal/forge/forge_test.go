package forge

import "testing"

func TestKeyBody(t *testing.T) {
	tests := map[string]string{
		"ssh-ed25519 AAAAC3Nza comment": "AAAAC3Nza",
		"  ssh-rsa AAAAB3Nza  ":         "AAAAB3Nza",
		"AAAAC3Nza":                     "AAAAC3Nza",
		"":                              "",
	}
	for input, want := range tests {
		if got := keyBody(input); got != want {
			t.Errorf("keyBody(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGitlabSlug(t *testing.T) {
	tests := map[string]string{
		"MyRepo":       "myrepo",
		"my repo":      "my-repo",
		"repo.name_ok": "repo.name_ok",
		"..weird..":    "weird",
		"":             "repo",
		"Project":      "project",
	}
	for input, want := range tests {
		if got := gitlabSlug(input); got != want {
			t.Errorf("gitlabSlug(%q) = %q, want %q", input, got, want)
		}
	}
}
