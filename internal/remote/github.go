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
)

type GitHubClient interface {
	CreatePullRequest(ctx context.Context, owner, repository, base, head, title, body string) (string, error)
	DeleteBranch(ctx context.Context, owner, repository, branch string) error
}

type githubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewGitHubClient(baseURL, token string, client *http.Client) GitHubClient {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &githubClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: client}
}

func ParseRepository(repository string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || !validRepositoryPart(parts[0]) || !validRepositoryPart(parts[1]) {
		return "", "", fmt.Errorf("repository must be owner/name")
	}
	return parts[0], parts[1], nil
}

func validRepositoryPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func (g *githubClient) CreatePullRequest(ctx context.Context, owner, repository, base, head, title, body string) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	})
	if err != nil {
		return "", fmt.Errorf("encode pull request: %w", err)
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls", g.baseURL, url.PathEscape(owner), url.PathEscape(repository))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create pull request request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	response, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("create pull request request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return "", fmt.Errorf("GitHub create pull request returned HTTP %d", response.StatusCode)
	}
	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode pull request response: %w", err)
	}
	if result.HTMLURL == "" {
		return "", fmt.Errorf("GitHub pull request response omitted html_url")
	}
	return result.HTMLURL, nil
}

func (g *githubClient) DeleteBranch(ctx context.Context, owner, repository, branch string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", g.baseURL, url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("delete branch request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete branch request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNoContent {
		return nil
	}
	_, _ = io.Copy(io.Discard, response.Body)
	return fmt.Errorf("GitHub delete branch returned HTTP %d", response.StatusCode)
}
