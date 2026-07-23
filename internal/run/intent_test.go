package run

import "testing"

func TestParseIntentDefaultsToFeat(t *testing.T) {
	intent, err := ParseIntent("")
	if err != nil || intent != IntentFeat {
		t.Fatalf("intent = %q, error = %v", intent, err)
	}
}

func TestParseIntentAcceptsTest(t *testing.T) {
	intent, err := ParseIntent(" test ")
	if err != nil || intent != IntentTest {
		t.Fatalf("intent = %q, error = %v", intent, err)
	}
}

func TestParseIntentRejectsUnknownMode(t *testing.T) {
	if _, err := ParseIntent("debug"); err == nil {
		t.Fatal("expected unknown mode error")
	}
}
