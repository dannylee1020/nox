package remote

import (
	"context"
	"time"
)

const (
	StateQueued     = "queued"
	StateRunning    = "running"
	StateCompleted  = "completed"
	StateNoChanges  = "no_changes"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
	StatePublishing = "publishing"
)

type RunRequest struct {
	Repository     string `json:"repository"`
	Mode           string `json:"mode,omitempty"`
	BaseBranch     string `json:"baseBranch"`
	BaseCommit     string `json:"baseCommit"`
	Title          string `json:"title"`
	Task           string `json:"task"`
	Validation     string `json:"validation"`
	Network        string `json:"network"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type RunStatus struct {
	RunID           string    `json:"runId"`
	Mode            string    `json:"mode,omitempty"`
	SourceIntegrity string    `json:"sourceIntegrity,omitempty"`
	ValidationCode  int       `json:"validationCode,omitempty"`
	State           string    `json:"state"`
	Stage           string    `json:"stage,omitempty"`
	Error           string    `json:"error,omitempty"`
	Branch          string    `json:"branch,omitempty"`
	Commit          string    `json:"commit,omitempty"`
	PullRequestURL  string    `json:"pullRequestUrl,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	CompletedAt     time.Time `json:"completedAt,omitempty"`
}

type Publication struct {
	Branch         string
	Commit         string
	PullRequestURL string
	NoChanges      bool
}

type ExecutionResult struct {
	Mode            string
	SourceIntegrity string
	ValidationCode  int
	Publication     *Publication
}

type Executor interface {
	Execute(ctx context.Context, runID string, request RunRequest) (ExecutionResult, error)
}
