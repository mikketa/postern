package challenge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestBlankOnSavedPanels runs the unpainted-grid detector over panels captured
// from live runs. It is skipped unless POSTERN_PANELS points at a directory of
// them, since those are not in the repository.
func TestBlankOnSavedPanels(t *testing.T) {
	dir := os.Getenv("POSTERN_PANELS")
	if dir == "" {
		t.Skip("set POSTERN_PANELS to a directory of captured panels")
	}

	metas, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var flagged, total int
	for _, mp := range metas {
		raw, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var meta struct{ Tiles string }
		if json.Unmarshal(raw, &meta) != nil || meta.Tiles == "" {
			continue
		}
		var tiles []Box
		for _, part := range strings.Split(meta.Tiles, ";") {
			n := strings.Split(part, ",")
			if len(n) != 4 {
				continue
			}
			f := make([]float64, 4)
			for i := range n {
				f[i], _ = strconv.ParseFloat(n[i], 64)
			}
			tiles = append(tiles, Box{X: f[0], Y: f[1], W: f[2], H: f[3]})
		}
		shot, err := os.ReadFile(strings.TrimSuffix(mp, ".json") + ".png")
		if err != nil {
			continue
		}
		total++
		if blank(shot, tiles) {
			flagged++
			t.Logf("unpainted: %s", filepath.Base(mp))
		}
	}
	t.Logf("%d unpainted out of %d", flagged, total)
}
