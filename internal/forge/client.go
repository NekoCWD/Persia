package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError is returned for non-2xx responses that are not 404.
type HTTPError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

type apiClient struct {
	baseURL string
	headers map[string]string
	hc      *http.Client
}

func newAPIClient(baseURL string, headers map[string]string, timeout time.Duration) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		headers: headers,
		hc: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *apiClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Message:    extractMessage(raw),
		}
	}

	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func extractMessage(raw []byte) string {
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Errors  []struct {
			Resource string `json:"resource"`
			Code     string `json:"code"`
			Field    string `json:"field"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	parts := make([]string, 0, 4)
	if err := json.Unmarshal(raw, &payload); err == nil {
		if payload.Error != "" {
			parts = append(parts, payload.Error)
		} else if payload.Message != "" {
			parts = append(parts, payload.Message)
		}
		for _, entry := range payload.Errors {
			value := entry.Message
			if value == "" {
				value = entry.Code
			}
			if entry.Field != "" && value != "" {
				value = entry.Field + ": " + value
			}
			if value != "" {
				parts = append(parts, value)
			}
		}
	}
	if len(parts) == 0 {
		s := strings.TrimSpace(string(raw))
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		return s
	}
	return strings.Join(parts, "; ")
}

func isStatus(err error, code int) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.StatusCode == code
}

func keyBody(publicKey string) string {
	fields := strings.Fields(strings.TrimSpace(publicKey))
	if len(fields) >= 2 {
		return fields[1]
	}
	return strings.TrimSpace(publicKey)
}
