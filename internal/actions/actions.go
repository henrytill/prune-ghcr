// Package actions implements the subset of the GitHub Actions toolkit this
// action uses: reading inputs, writing outputs, and the workflow commands that
// produce log lines and mask secrets.
package actions

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"strings"
)

// out is the stream workflow commands are written to. Tests replace it.
var out io.Writer = os.Stdout

// Input returns the value of an action input.
//
// The runner exposes inputs as INPUT_<NAME>, uppercased with spaces turned into
// underscores; hyphens are left alone, so min-age-hours is INPUT_MIN-AGE-HOURS.
func Input(name string) string {
	key := "INPUT_" + strings.ToUpper(strings.ReplaceAll(name, " ", "_"))
	return strings.TrimSpace(os.Getenv(key))
}

// BoolInput returns the value of a boolean action input.
//
// Only the YAML 1.2 core schema spellings are accepted, matching
// core.getBooleanInput. strconv.ParseBool is deliberately not used: it also
// accepts 1, 0, t and f, so values the action rejects today would silently
// start working.
func BoolInput(name string) (bool, error) {
	switch value := Input(name); value {
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	default:
		return false, fmt.Errorf(
			"input %s must be one of true|True|TRUE|false|False|FALSE, got %q", name, value)
	}
}

// escapeData escapes a workflow command's message.
//
// The replacements happen in a single pass, so the % introduced by an earlier
// replacement is not escaped again.
var escapeData = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace

// issue writes a workflow command. Only the property-less form is implemented,
// because it is the only one this action uses; adding properties would mean a
// second escaping table that also covers : and comma.
// A failed write to the log is deliberately dropped: there is nowhere left to
// report it, and the run should not fail because an annotation did not print.
func issue(command, message string) {
	_, _ = fmt.Fprintf(out, "::%s::%s\n", command, escapeData(message))
}

// SetSecret registers a value to be masked in the log.
func SetSecret(secret string) { issue("add-mask", secret) }

// Warning writes a warning annotation.
func Warning(message string) { issue("warning", message) }

// Error writes an error annotation.
func Error(message string) { issue("error", message) }

// Info writes a plain log line.
func Info(message string) { _, _ = fmt.Fprintln(out, message) }

// SetOutput sets an action output by appending to the file named by
// GITHUB_OUTPUT.
//
// The heredoc form is used rather than name=value so that a value containing a
// newline cannot forge additional outputs.
func SetOutput(name string, value string) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return fmt.Errorf("GITHUB_OUTPUT is not set")
	}

	delimiter := "ghadelimiter_" + rand.Text()
	if strings.Contains(name, delimiter) || strings.Contains(value, delimiter) {
		return fmt.Errorf("output %s contains the generated delimiter", name)
	}

	// The path is GITHUB_OUTPUT, set by the runner, not by any action input.
	// #nosec G304 G703 -- not attacker-controlled.
	//
	// The mode applies only if the file does not exist, which on a runner it
	// always does: the runner creates it, and O_CREATE is here for local runs
	// outside one.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}

	if _, err := fmt.Fprintf(file, "%s<<%s\n%s\n%s\n", name, delimiter, value, delimiter); err != nil {
		_ = file.Close()
		return fmt.Errorf("writing to GITHUB_OUTPUT: %w", err)
	}
	return file.Close()
}
