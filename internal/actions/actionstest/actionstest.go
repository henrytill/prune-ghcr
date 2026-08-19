// Package actionstest reads back what the actions package writes.
//
// It exists so that the heredoc layout of $GITHUB_OUTPUT has one owner.
// SetOutput is the only thing that writes that format, and a test in another
// package that parses it becomes a second definition of it -- one that fails,
// when the format changes, by pointing at whichever package happened to be
// reading rather than at the package that decides.
package actionstest

import (
	"os"
	"strings"
	"testing"
)

// Outputs returns the outputs written to path, which is a file named by
// $GITHUB_OUTPUT.
//
// Each output occupies three lines: the name and a generated delimiter, the
// value, and the delimiter again. A later write of the same name replaces the
// earlier one, the way the runner resolves them.
func Outputs(t *testing.T, path string) map[string]string {
	t.Helper()

	// #nosec G304 -- the path is the caller's own t.TempDir().
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines)%3 != 0 {
		t.Fatalf("%s is not whole heredocs:\n%s", path, contents)
	}

	outputs := make(map[string]string, len(lines)/3)
	for i := 0; i < len(lines); i += 3 {
		name, delimiter, ok := strings.Cut(lines[i], "<<")
		if !ok {
			t.Fatalf("line %d of %s is not a heredoc opener: %q", i+1, path, lines[i])
		}
		if lines[i+2] != delimiter {
			t.Fatalf("output %s is not closed by its delimiter: %q", name, lines[i+2])
		}
		outputs[name] = lines[i+1]
	}
	return outputs
}
