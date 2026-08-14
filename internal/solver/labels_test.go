package solver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A label file is only worth what the grading behind it is worth. reCAPTCHA
// grades the whole challenge, so a token means every answer was accepted and a
// failure means at least one was wrong with no way to tell which — which is why
// only the success path calls this.
func TestAnswersAreWrittenAsLabels(t *testing.T) {
	dir := t.TempDir()

	if err := recordAnswers(dir, map[string][]int{"1786": {1, 4}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A second challenge adds to the file rather than replacing it: a fleet
	// left running should accumulate a corpus, not keep overwriting one.
	if err := recordAnswers(dir, map[string][]int{"1787": nil}); err != nil {
		t.Fatalf("record again: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "labels.json"))
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
	// "none of these" is a real answer and has to survive the round trip as an
	// empty list rather than a missing entry.
	if got, ok := labels["1787"]; !ok || len(got) != 0 {
		t.Fatalf("an empty answer came back as %v, ok=%v", got, ok)
	}
}

func TestNoPanelDirectoryIsNotAnError(t *testing.T) {
	if err := recordAnswers("", map[string][]int{"x": {1}}); err != nil {
		t.Fatalf("recording with nowhere to record should do nothing: %v", err)
	}
}
