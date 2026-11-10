package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateRoomFallsBackToSeparateInvites(t *testing.T) {
	createCalls := 0
	var invited []string
	var sent bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_matrix/client/v3/createRoom":
			createCalls++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["invite"]; ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"bad invite"}`))
				return
			}
			_, _ = w.Write([]byte(`{"room_id":"!room:test"}`))
		case strings.HasSuffix(r.URL.Path, "/invite"):
			var body struct {
				UserID string `json:"user_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			invited = append(invited, body.UserID)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			sent = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, "token", "", 5*time.Second)
	roomID, err := client.CreateRoom(context.Background(), "room", "topic", []string{"@neko:test"}, false)
	if err != nil {
		t.Fatalf("CreateRoom() error: %v", err)
	}
	if roomID != "!room:test" {
		t.Fatalf("unexpected room ID: %q", roomID)
	}
	if createCalls != 2 {
		t.Fatalf("expected 2 createRoom calls, got %d", createCalls)
	}
	if len(invited) != 1 || invited[0] != "@neko:test" {
		t.Fatalf("unexpected invites: %v", invited)
	}

	if err := client.Send(context.Background(), roomID, Message{Text: "hello", HTML: "<b>hello</b>"}); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !sent {
		t.Fatal("message was not sent")
	}
}
