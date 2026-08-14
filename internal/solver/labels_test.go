package solver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// What is recorded is only worth what the grading behind it is worth. A token
// grades the squares that were ticked; it says nothing about the rest, since
// reCAPTCHA passes incomplete answers — so this file holds positives and
// nothing else.
func TestAcceptedAnswersAreRecorded(t *testing.T) {
	dir := t.TempDir()

	if err := recordAnswers(dir, map[string][]int{"1786": {1, 4}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A second challenge adds to the file rather than replacing it: a fleet
	// left running should accumulate a corpus, not keep overwriting one.
	if err := recordAnswers(dir, map[string][]int{"1787": {2}}); err != nil {
		t.Fatalf("record again: %v", err)
	}
	// A round where nothing was ticked says the solver saw nothing, not that
	// the grid held nothing. Recording it as an empty answer would be a claim
	// the token does not support.
	if err := recordAnswers(dir, map[string][]int{"1788": nil}); err != nil {
		t.Fatalf("record empty: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "answers.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var labels map[string][]int
	if err := json.Unmarshal(body, &labels); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(labels) != 2 {
		t.Fatalf("labels hold %d panels, want 2: %v", len(labels), labels)
	}
	if got := labels["1786"]; len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("panel 1786 is labelled %v, want [1 4]", got)
	}
	if _, ok := labels["1788"]; ok {
		t.Fatal("a round with nothing ticked was recorded as if it held nothing")
	}
}

func TestNoPanelDirectoryIsNotAnError(t *testing.T) {
	if err := recordAnswers("", map[string][]int{"x": {1}}); err != nil {
		t.Fatalf("recording with nowhere to record should do nothing: %v", err)
	}
}
