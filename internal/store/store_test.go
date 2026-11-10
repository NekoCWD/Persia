package store

import (
	"context"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer st.Close()

	if err := st.SaveRoom(ctx, "demo/app", "!room:example.org"); err != nil {
		t.Fatal(err)
	}
	room, ok, err := st.Room(ctx, "demo/app")
	if err != nil || !ok || room != "!room:example.org" {
		t.Fatalf("Room() = %q, %v, %v", room, ok, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	item := Item{
		Source:    "local",
		RepoKey:   "demo/app",
		Kind:      "issue",
		Number:    42,
		State:     "open",
		Title:     "Something",
		Comments:  2,
		UpdatedAt: now,
	}
	if err := st.SaveItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetItem(ctx, "local", "demo/app", "issue", 42)
	if err != nil || !ok {
		t.Fatalf("GetItem() = %v, %v", ok, err)
	}
	if got.Title != item.Title || got.Comments != item.Comments || !got.UpdatedAt.Equal(now) {
		t.Errorf("unexpected item: %+v", got)
	}

	pipeline := Pipeline{
		Source:    "github",
		RepoKey:   "demo/app",
		ID:        "123",
		Name:      "CI",
		Status:    "failure",
		URL:       "https://example.org/run/123",
		Ref:       "main",
		SHA:       "abcdef",
		UpdatedAt: now,
	}
	if err := st.SavePipeline(ctx, pipeline); err != nil {
		t.Fatal(err)
	}
	gotPipeline, ok, err := st.GetPipeline(ctx, "github", "demo/app", "123")
	if err != nil || !ok || gotPipeline.Status != "failure" {
		t.Fatalf("GetPipeline() = %+v, %v, %v", gotPipeline, ok, err)
	}

	if err := st.SaveMirror(ctx, Mirror{
		RepoKey:   "demo/app",
		Target:    "github",
		Address:   "git@github.com:demo/app.git",
		PublicKey: "ssh-ed25519 AAAATEST",
	}); err != nil {
		t.Fatal(err)
	}
	mirrors, err := st.Mirrors(ctx, "demo/app")
	if err != nil {
		t.Fatal(err)
	}
	if mirrors["github"].PublicKey != "ssh-ed25519 AAAATEST" {
		t.Errorf("unexpected mirror: %+v", mirrors["github"])
	}

	if seeded, err := st.IsSeeded(ctx, "issues|local|demo/app"); err != nil || seeded {
		t.Fatalf("IsSeeded() = %v, %v", seeded, err)
	}
	if err := st.MarkSeeded(ctx, "issues|local|demo/app"); err != nil {
		t.Fatal(err)
	}
	if seeded, err := st.IsSeeded(ctx, "issues|local|demo/app"); err != nil || !seeded {
		t.Fatalf("IsSeeded() = %v, %v", seeded, err)
	}
}
