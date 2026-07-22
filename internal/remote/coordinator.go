package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nox-dev/nox/internal/execx"
	"github.com/nox-dev/nox/internal/gitx"
	"github.com/nox-dev/nox/internal/run"
)

type CoordinatorConfig struct {
	StateRoot     string
	GitHubToken   string
	GitHubAPIURL  string
	CodexHome     string
	GitName       string
	GitEmail      string
	GitHubClient  GitHubClient
	CommandRunner execx.CommandRunner
}

type Coordinator struct {
	config CoordinatorConfig
}

func NewCoordinator(config CoordinatorConfig) *Coordinator {
	if config.StateRoot == "" {
		home, _ := os.UserHomeDir()
		config.StateRoot = filepath.Join(home, ".nox", "remote")
	}
	if config.GitName == "" {
		config.GitName = "Nox Worker"
	}
	if config.GitEmail == "" {
		config.GitEmail = "nox@localhost"
	}
	if config.CommandRunner == nil {
		config.CommandRunner = execx.Runner{}
	}
	if config.GitHubClient == nil {
		config.GitHubClient = NewGitHubClient(config.GitHubAPIURL, config.GitHubToken, nil)
	}
	return &Coordinator{config: config}
}

func (c *Coordinator) Execute(parent context.Context, runID string, request RunRequest) (Publication, error) {
	owner, repository, err := ParseRepository(request.Repository)
	if err != nil {
		return Publication{}, err
	}
	if len(request.BaseCommit) != 40 {
		return Publication{}, fmt.Errorf("baseCommit must be a 40-character commit SHA")
	}
	if request.BaseBranch == "" || request.Title == "" || request.Task == "" || request.Validation == "" {
		return Publication{}, fmt.Errorf("baseBranch, title, task, and validation are required")
	}
	if request.Network != "online" && request.Network != "none" {
		return Publication{}, fmt.Errorf("network must be online or none")
	}
	timeout := time.Duration(request.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if err := os.MkdirAll(c.config.StateRoot, 0o700); err != nil {
		return Publication{}, fmt.Errorf("create remote state root: %w", err)
	}
	credentials, err := newGitCredentials(c.config.StateRoot, runID, c.config.GitHubToken)
	if err != nil {
		return Publication{}, err
	}
	defer credentials.Close()
	git := gitx.Git{Runner: c.config.CommandRunner, Env: credentials.Env()}
	source := filepath.Join(c.config.StateRoot, "sources", runID)
	defer os.RemoveAll(source)
	remoteURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repository)
	if err := git.CloneAt(ctx, remoteURL, request.BaseCommit, source); err != nil {
		return Publication{}, err
	}
	if err := configureIdentity(ctx, git, source, c.config.GitName, c.config.GitEmail); err != nil {
		return Publication{}, err
	}

	orchestrator := run.New()
	orchestrator.Git = git
	result, err := orchestrator.Launch(ctx, run.Config{
		RunID:           runID,
		Repo:            source,
		RepositoryLabel: request.Repository,
		From:            request.BaseCommit,
		OutputBranch:    "nox/" + runID,
		Task:            request.Task,
		Validation:      request.Validation,
		Network:         request.Network,
		CodexHome:       c.config.CodexHome,
		StateRoot:       filepath.Join(c.config.StateRoot, "runs"),
		Timeout:         timeout,
	})
	if err != nil {
		return Publication{}, err
	}
	if result.NoChanges {
		return Publication{Branch: "nox/" + runID, NoChanges: true}, nil
	}
	branch := result.Metadata.OutputBranch
	if err := git.Push(ctx, source, "origin", branch); err != nil {
		return Publication{}, err
	}
	body := fmt.Sprintf("## Nox remote run\n\n- Run: `%s`\n- Base commit: `%s`\n- Result commit: `%s`\n- Validation: `%s`\n- Isolation: Docker with gVisor `runsc`\n", runID, result.Metadata.BaseSHA, result.Metadata.ResultSHA, request.Validation)
	pullRequestURL, err := c.config.GitHubClient.CreatePullRequest(ctx, owner, repository, request.BaseBranch, branch, request.Title, body)
	if err != nil {
		_ = c.config.GitHubClient.DeleteBranch(context.Background(), owner, repository, branch)
		return Publication{}, err
	}
	return Publication{Branch: branch, Commit: result.Metadata.ResultSHA, PullRequestURL: pullRequestURL}, nil
}

func configureIdentity(ctx context.Context, git gitx.Git, repo, name, email string) error {
	for _, setting := range [][2]string{{"user.name", name}, {"user.email", email}} {
		if _, err := git.Runner.Run(ctx, execx.Command{
			Name: "git", Args: []string{"config", setting[0], setting[1]}, Dir: repo, Env: git.Env,
		}); err != nil {
			return fmt.Errorf("configure Git %s: %w", setting[0], err)
		}
	}
	return nil
}

type gitCredentials struct {
	directory string
	token     string
}

func newGitCredentials(root, runID, token string) (*gitCredentials, error) {
	directory := filepath.Join(root, "credentials", runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Git credential directory: %w", err)
	}
	askpass := filepath.Join(directory, "askpass.sh")
	contents := "#!/bin/sh\ncase \"$1\" in\n  *Username*) printf '%s\\n' x-access-token ;;\n  *) printf '%s\\n' \"$NOX_GITHUB_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(contents), 0o700); err != nil {
		return nil, fmt.Errorf("write Git credential helper: %w", err)
	}
	return &gitCredentials{directory: directory, token: token}, nil
}

func (c *gitCredentials) Env() []string {
	return execx.CurrentEnv(
		"GIT_ASKPASS="+filepath.Join(c.directory, "askpass.sh"),
		"GIT_TERMINAL_PROMPT=0",
		"NOX_GITHUB_TOKEN="+c.token,
	)
}

func (c *gitCredentials) Close() {
	_ = os.RemoveAll(c.directory)
}
