package run

import (
	"fmt"
	"strings"
)

// Intent selects the trusted outcome of a sandbox run.
type Intent string

const (
	IntentFeat Intent = "feat"
	IntentTest Intent = "test"
)

func ParseIntent(value string) (Intent, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return IntentFeat, nil
	}
	intent := Intent(value)
	if intent != IntentFeat && intent != IntentTest {
		return "", fmt.Errorf("mode must be feat or test, got %q", value)
	}
	return intent, nil
}

func (i Intent) IsTest() bool { return i == IntentTest }
