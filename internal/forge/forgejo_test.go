package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newForgejoTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login":"demo"}`))
		case r.URL.Path == "/api/v1/repos/search":
			if r.URL.Query().Get("topic") != "true" {
				t.Errorf("expected topic search")
			}
			_, _ = w.Write([]byte(`{"data":[
				{"id":1,"name":"persia","full_name":"demo/app","html_url":"https://git.test/demo/app","topics":["persia-managed"],"owner":{"login":"demo"}},
				{"id":2,"name":"other","full_name":"demo/other","html_url":"https://git.test/demo/other","topics":["other"],"owner":{"login":"demo"}},
				{"id":3,"name":"shared","full_name":"alice/shared","html_url":"https://git.test/alice/shared","topics":["persia-managed"],"owner":{"login":"alice"}},
				{"id":4,"name":"mirror","full_name":"team/mirror","html_url":"https://git.test/team/mirror","topics":["persia-managed"],"owner":{"login":"team"}}
			]}`))
		case r.URL.Path == "/api/v1/repos/demo/newrepo" && r.Method == "GET":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		case r.URL.Path == "/api/v1/orgs/demo":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		case r.URL.Path == "/api/v1/user/repos" && r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "newrepo" {
				t.Errorf("unexpected repo name: %v", body["name"])
			}
			_, _ = w.Write([]byte(`{"id":3,"name":"newrepo","full_name":"demo/newrepo","html_url":"https://git.test/demo/newrepo","owner":{"login":"demo"}}`))
		case r.URL.Path == "/api/v1/repos/demo/app/issues":
			_, _ = w.Write([]byte(`[
				{"number":1,"title":"bug","state":"open","html_url":"https://git.test/demo/app/issues/1","comments":2,"updated_at":"2026-01-02T03:04:05Z","user":{"login":"alice"}},
				{"number":2,"title":"feature","state":"closed","html_url":"https://git.test/demo/app/pulls/2","comments":0,"updated_at":"2026-01-02T03:04:05Z","user":{"login":"bob"},"pull_request":{"merged":true,"html_url":"https://git.test/demo/app/pulls/2"}}
			]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestForgejoManagedReposAndEnsureRepo(t *testing.T) {
	server := newForgejoTestServer(t)
	defer server.Close()

	client := NewForgejo("altlinux", server.URL, "token", "", false, 5*time.Second)
	repos, err := client.ListManagedRepos(context.Background(), "persia-managed", nil)
	if err != nil {
		t.Fatalf("ListManagedRepos() error: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("unexpected managed repos: %+v", repos)
	}
	if repos[0].FullName != "alice/shared" || repos[1].FullName != "demo/app" || repos[2].FullName != "team/mirror" {
		t.Fatalf("unexpected managed repos: %+v", repos)
	}

	ref, err := client.EnsureRepo(context.Background(), Repo{Name: "newrepo"})
	if err != nil {
		t.Fatalf("EnsureRepo() error: %v", err)
	}
	if ref.Owner != "demo" || ref.Name != "newrepo" || ref.ID != "3" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

func TestForgejoManagedReposOwnerFilter(t *testing.T) {
	server := newForgejoTestServer(t)
	defer server.Close()
	client := NewForgejo("local", server.URL, "token", "", false, 5*time.Second)

	tests := []struct {
		name   string
		owners []string
		want   []string
	}{
		{
			name:   "single owner",
			owners: []string{"demo"},
			want:   []string{"demo/app"},
		},
		{
			name:   "case insensitive",
			owners: []string{"DEMO"},
			want:   []string{"demo/app"},
		},
		{
			name:   "several owners",
			owners: []string{"alice", "team"},
			want:   []string{"alice/shared", "team/mirror"},
		},
		{
			name:   "wildcard disables filter",
			owners: []string{"*", "demo"},
			want:   []string{"alice/shared", "demo/app", "team/mirror"},
		},
		{
			name:   "no match",
			owners: []string{"bob"},
			want:   nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repos, err := client.ListManagedRepos(context.Background(), "persia-managed", test.owners)
			if err != nil {
				t.Fatalf("ListManagedRepos() error: %v", err)
			}
			got := make([]string, 0, len(repos))
			for _, repo := range repos {
				got = append(got, repo.FullName)
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestForgejoListIssuesClassifiesPullRequests(t *testing.T) {
	server := newForgejoTestServer(t)
	defer server.Close()

	client := NewForgejo("altlinux", server.URL, "token", "demo", false, 5*time.Second)
	issues, err := client.ListIssues(context.Background(), Ref{Owner: "demo", Name: "app"})
	if err != nil {
		t.Fatalf("ListIssues() error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Kind != "issue" {
		t.Errorf("expected issue, got %q", issues[0].Kind)
	}
	if issues[1].Kind != "pull_request" || issues[1].State != "merged" {
		t.Errorf("unexpected pull request: %+v", issues[1])
	}
}
