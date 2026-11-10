// Package matrix implements the small subset of the Matrix client-server API
// that Persia needs: whoami, room creation, invites and HTML messages.
package matrix

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a Matrix bot client.
type Client struct {
	baseURL string
	token   string
	userID  string
	hc      *http.Client
}

// New creates a Matrix client for the given homeserver and access token.
func New(homeserver, accessToken, userID string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(homeserver, "/") + "/_matrix/client/v3",
		token:   accessToken,
		userID:  userID,
		hc:      &http.Client{Timeout: timeout},
	}
}

// UserID returns the configured bot user ID (it may be empty before WhoAmI).
func (c *Client) UserID() string { return c.userID }

// SetUserID records the bot user ID after a successful WhoAmI.
func (c *Client) SetUserID(userID string) { c.userID = userID }

// Message is a formatted Matrix message.
type Message struct {
	Text string
	HTML string
}

// WhoAmI verifies the access token and returns the bot user ID.
func (c *Client) WhoAmI(ctx context.Context) (string, error) {
	var response struct {
		UserID string `json:"user_id"`
	}
	if err := c.do(ctx, "GET", "/account/whoami", nil, nil, &response); err != nil {
		return "", err
	}
	if response.UserID == "" {
		return "", fmt.Errorf("matrix returned an empty user_id")
	}
	c.userID = response.UserID
	return response.UserID, nil
}

// CreateRoom creates a room, invites users, returns room ID.
func (c *Client) CreateRoom(ctx context.Context, name, topic string, invite []string, public bool) (string, error) {
	body := map[string]any{
		"name":       name,
		"topic":      topic,
		"preset":     "private_chat",
		"visibility": "private",
	}
	if public {
		body["preset"] = "public_chat"
		body["visibility"] = "public"
	}
	if len(invite) > 0 {
		body["invite"] = invite
	}

	var response struct {
		RoomID string `json:"room_id"`
	}
	err := c.do(ctx, "POST", "/createRoom", nil, body, &response)
	if err != nil && len(invite) > 0 {
		// Some homeservers reject createRoom with invites (for example when a
		// user does not exist). Create the room and try invites separately.
		delete(body, "invite")
		if retryErr := c.do(ctx, "POST", "/createRoom", nil, body, &response); retryErr == nil {
			for _, userID := range invite {
				if inviteErr := c.Invite(ctx, response.RoomID, userID); inviteErr != nil {
					return response.RoomID, fmt.Errorf("invite %s: %w", userID, inviteErr)
				}
			}
			return response.RoomID, nil
		}
	}
	if err != nil {
		return "", err
	}
	if response.RoomID == "" {
		return "", fmt.Errorf("matrix returned an empty room_id")
	}
	return response.RoomID, nil
}

// Invite invites a user to a room.
func (c *Client) Invite(ctx context.Context, roomID, userID string) error {
	body := map[string]any{"user_id": userID}
	return c.do(ctx, "POST", "/rooms/"+url.PathEscape(roomID)+"/invite", nil, body, nil)
}

// Send sends a formatted HTML message to a room.
func (c *Client) Send(ctx context.Context, roomID string, message Message) error {
	body := map[string]any{
		"msgtype": "m.text",
		"body":    message.Text,
	}
	if message.HTML != "" {
		body["format"] = "org.matrix.custom.html"
		body["formatted_body"] = message.HTML
	}

	txnID, err := randomID()
	if err != nil {
		return err
	}
	path := "/rooms/" + url.PathEscape(roomID) + "/send/m.room.message/" + url.PathEscape(txnID)
	return c.do(ctx, "PUT", path, nil, body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal matrix request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("create matrix request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read matrix response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var payload struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(raw, &payload)
		message := strings.TrimSpace(payload.Error)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("%s %s: HTTP %d: %s %s", method, path, resp.StatusCode, payload.ErrCode, message)
	}

	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode matrix response: %w", err)
	}
	return nil
}

func randomID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate transaction id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// SetRoomAlias registers an alias for a room.
func (c *Client) SetRoomAlias(ctx context.Context, alias, roomID string) error {
	if strings.TrimSpace(alias) == "" || strings.TrimSpace(roomID) == "" {
		return nil
	}
	body := map[string]any{"room_id": roomID}
	err := c.do(ctx, "PUT", "/directory/room/"+url.PathEscape(alias), nil, body, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

// PutState writes a room state event.
func (c *Client) PutState(ctx context.Context, roomID, eventType string, content any) error {
	path := "/rooms/" + url.PathEscape(roomID) + "/state/" + eventType
	return c.do(ctx, "PUT", path, nil, content, nil)
}

// State returns a room state event content.
func (c *Client) State(ctx context.Context, roomID, eventType string, out any) error {
	path := "/rooms/" + url.PathEscape(roomID) + "/state/" + eventType
	return c.do(ctx, "GET", path, nil, nil, out)
}

// RoomAliases returns aliases registered for a room.
func (c *Client) RoomAliases(ctx context.Context, roomID string) ([]string, error) {
	if strings.TrimSpace(roomID) == "" {
		return nil, nil
	}
	var response struct {
		Aliases []string `json:"aliases"`
	}
	if err := c.do(ctx, "GET", "/rooms/"+url.PathEscape(roomID)+"/aliases", nil, nil, &response); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not in room") {
			return nil, nil
		}
		return nil, err
	}
	return response.Aliases, nil
}

// JoinRule returns the room's join rule (`public`, `invite`, etc.).
func (c *Client) JoinRule(ctx context.Context, roomID string) (string, error) {
	if strings.TrimSpace(roomID) == "" {
		return "", nil
	}
	path := "/rooms/" + url.PathEscape(roomID) + "/state/m.room.join_rules"
	var response struct {
		JoinRule string `json:"join_rule"`
	}
	if err := c.do(ctx, "GET", path, nil, nil, &response); err != nil {
		return "", err
	}
	return response.JoinRule, nil
}

// Leave leaves a Matrix room if permitted by the homeserver.

// Leave leaves a Matrix room if permitted by the homeserver.
func (c *Client) Leave(ctx context.Context, roomID string) error {
	if strings.TrimSpace(roomID) == "" {
		return nil
	}
	path := "/rooms/" + url.PathEscape(roomID) + "/leave"
	return c.do(ctx, "POST", path, nil, map[string]any{}, nil)
}
