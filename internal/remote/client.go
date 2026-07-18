package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultPollInterval = 2 * time.Second

// Client submits and monitors runs on a trusted remote Nox server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	token = strings.TrimSpace(token)
	if baseURL == "" {
		return nil, fmt.Errorf("remote URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("remote URL must be an absolute HTTP or HTTPS URL without credentials or query parameters")
	}
	if token == "" {
		return nil, fmt.Errorf("remote API token is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}, nil
}

func (c *Client) Submit(ctx context.Context, request RunRequest) (RunStatus, error) {
	var status RunStatus
	if err := c.doJSON(ctx, http.MethodPost, "/v1/runs", request, &status); err != nil {
		return RunStatus{}, err
	}
	return status, nil
}

func (c *Client) Status(ctx context.Context, runID string) (RunStatus, error) {
	if err := validateClientRunID(runID); err != nil {
		return RunStatus{}, err
	}
	var status RunStatus
	path := "/v1/runs/" + url.PathEscape(runID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &status); err != nil {
		return RunStatus{}, err
	}
	return status, nil
}

func (c *Client) Cancel(ctx context.Context, runID string) error {
	if err := validateClientRunID(runID); err != nil {
		return err
	}
	path := "/v1/runs/" + url.PathEscape(runID) + "/cancel"
	return c.doJSON(ctx, http.MethodPost, path, nil, nil)
}

// Wait polls a submitted run until the server reports a terminal state.
func (c *Client) Wait(ctx context.Context, initial RunStatus, interval time.Duration, onStatus func(RunStatus)) (RunStatus, error) {
	if initial.RunID == "" {
		return RunStatus{}, fmt.Errorf("remote response omitted run ID")
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}
	current := initial
	if onStatus != nil {
		onStatus(current)
	}
	for !IsTerminalState(current.State) {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return current, ctx.Err()
		case <-timer.C:
		}
		status, err := c.Status(ctx, current.RunID)
		if err != nil {
			return current, err
		}
		current = status
		if onStatus != nil {
			onStatus(current)
		}
	}
	return current, nil
}

func IsTerminalState(state string) bool {
	switch state {
	case StateCompleted, StateNoChanges, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func validateClientRunID(runID string) error {
	if runID == "" || len(runID) > 64 {
		return fmt.Errorf("invalid remote run ID")
	}
	for _, character := range runID {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return fmt.Errorf("invalid remote run ID")
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, destination any) error {
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode remote request: %w", err)
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, input)
	if err != nil {
		return fmt.Errorf("create remote request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("remote request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := struct {
			Error string `json:"error"`
		}{}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&message)
		if message.Error == "" {
			message.Error = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("remote request returned HTTP %d: %s", response.StatusCode, strings.ReplaceAll(message.Error, c.token, "[redacted]"))
	}
	if destination == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode remote response: %w", err)
	}
	return nil
}
