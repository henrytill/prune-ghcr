package actions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capture swaps the command stream for the duration of a test.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := out
	out = buffer
	t.Cleanup(func() { out = previous })
	return buffer
}

func TestInputReadsTheRunnersEnvironment(t *testing.T) {
	// Hyphens are preserved and spaces become underscores.
	t.Setenv("INPUT_MIN-AGE-HOURS", "  12  ")
	t.Setenv("INPUT_TWO_WORDS", "value")

	if got := Input("min-age-hours"); got != "12" {
		t.Errorf("Input = %q, want 12 (trimmed)", got)
	}
	if got := Input("two words"); got != "value" {
		t.Errorf("Input = %q, want value", got)
	}
	if got := Input("absent"); got != "" {
		t.Errorf("Input = %q, want empty", got)
	}
}

func TestBoolInputAcceptsOnlyTheActionsSpellings(t *testing.T) {
	accepted := map[string]bool{
		"true": true, "True": true, "TRUE": true,
		"false": false, "False": false, "FALSE": false,
	}
	for value, want := range accepted {
		t.Setenv("INPUT_DRY-RUN", value)
		got, err := BoolInput("dry-run")
		if err != nil {
			t.Errorf("BoolInput(%q): %v", value, err)
		}
		if got != want {
			t.Errorf("BoolInput(%q) = %v, want %v", value, got, want)
		}
	}

	// strconv.ParseBool would accept these; core.getBooleanInput does not.
	for _, value := range []string{"1", "0", "t", "f", "yes", ""} {
		t.Setenv("INPUT_DRY-RUN", value)
		if _, err := BoolInput("dry-run"); err == nil {
			t.Errorf("BoolInput(%q) = nil error, want a failure", value)
		}
	}
}

func TestCommandsEscapeTheirMessage(t *testing.T) {
	buffer := capture(t)

	Error("100% broken\r\nsecond line")

	want := "::error::100%25 broken%0D%0Asecond line\n"
	if got := buffer.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestEscapingDoesNotDoubleEscapeItsOwnOutput(t *testing.T) {
	buffer := capture(t)

	// The %0A here is literal text, not a newline: it must survive as %250A.
	Info("literal")
	Warning("%0A")

	want := "literal\n::warning::%250A\n"
	if got := buffer.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestSetSecretMasksTheValue(t *testing.T) {
	buffer := capture(t)

	SetSecret("ghp_secret")

	if want := "::add-mask::ghp_secret\n"; buffer.String() != want {
		t.Errorf("output = %q, want %q", buffer.String(), want)
	}
}

func TestSetOutputAppendsAHeredocToGithubOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	t.Setenv("GITHUB_OUTPUT", path)

	if err := SetOutput("deleted", "3"); err != nil {
		t.Fatalf("SetOutput: %v", err)
	}
	if err := SetOutput("kept", "7"); err != nil {
		t.Fatalf("SetOutput: %v", err)
	}

	// #nosec G304 G703 -- the path is this test's own t.TempDir().
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(contents), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), contents)
	}
	if !strings.HasPrefix(lines[0], "deleted<<ghadelimiter_") {
		t.Errorf("first line = %q, want a deleted heredoc", lines[0])
	}
	if lines[1] != "3" || lines[4] != "7" {
		t.Errorf("values = %q and %q, want 3 and 7", lines[1], lines[4])
	}
	if lines[0] == lines[3] {
		t.Error("both outputs share a delimiter, want one generated per call")
	}
}

func TestSetOutputFailsWhenGithubOutputIsUnset(t *testing.T) {
	t.Setenv("GITHUB_OUTPUT", "")

	if err := SetOutput("deleted", "3"); err == nil {
		t.Error("SetOutput = nil error, want a failure")
	}
}
