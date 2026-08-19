package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henrytill/prune-ghcr/internal/actions/actionstest"
	"github.com/henrytill/prune-ghcr/internal/api"
	"github.com/henrytill/prune-ghcr/internal/prune"
)

func TestParseMinAge(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{input: "0"},
		{input: "1", want: time.Hour},
		{input: "0.5", want: 30 * time.Minute},
		{input: "1e1", want: 10 * time.Hour},
		// An unset expression reaches this input as an empty string, and a
		// workflow that does so is green today.
		{input: ""},
		{input: "-1", wantErr: true},
		{input: "abc", wantErr: true},
		// ParseFloat accepts these, and they are not ages.
		{input: "NaN", wantErr: true},
		{input: "Inf", wantErr: true},
		{input: "-Inf", wantErr: true},
		// The largest hour count that fits in a time.Duration; anything above
		// it would overflow into a negative min-age and silently skip every
		// version.
		{input: "2562047", want: 2562047 * time.Hour},
		{input: "2562048", wantErr: true},
		{input: "1e15", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := parseMinAge(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseMinAge(%q) = %v, nil, want an error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMinAge(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Errorf("parseMinAge(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

// setInputs puts the action's inputs in the environment the way the runner
// does, clearing every one it is not given so that a developer's own INPUT_*
// cannot decide a case.
//
// Hyphens are preserved rather than turned into underscores, which is why
// min-age-hours arrives as INPUT_MIN-AGE-HOURS.
func setInputs(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range []string{"token", "owner", "package", "dry-run", "min-age-hours"} {
		t.Setenv("INPUT_"+strings.ToUpper(name), values[name])
	}
}

func TestReadInputsValidatesWhatTheActionWasGiven(t *testing.T) {
	completeValues := map[string]string{
		"token":         "ghp_token",
		"owner":         "henrytill",
		"package":       "devcontainer-debian",
		"dry-run":       "false",
		"min-age-hours": "24",
	}

	with := func(name, value string) map[string]string {
		values := maps.Clone(completeValues)
		values[name] = value
		return values
	}

	complete := inputs{
		token: "ghp_token",
		options: prune.Options{
			Owner:       "henrytill",
			PackageName: "devcontainer-debian",
			MinAge:      24 * time.Hour,
		},
	}

	// The same treatment the values get, so that a case shows the one field it
	// is about rather than a literal a reader has to diff against its siblings.
	wantWith := func(change func(*inputs)) inputs {
		want := complete
		change(&want)
		return want
	}

	tests := []struct {
		name    string
		values  map[string]string
		want    inputs
		wantErr string
	}{
		{name: "a complete set", values: completeValues, want: complete},
		{
			// A secret pasted with a newline, or with one inside it: the token
			// that reaches the Authorization header must carry neither.
			name:   "a token with whitespace in it",
			values: with("token", " ghp_to ken\n"),
			want:   complete,
		},
		{
			name:   "a dry run",
			values: with("dry-run", "true"),
			want:   wantWith(func(in *inputs) { in.options.DryRun = true }),
		},
		{
			// An unset expression reaches this input as an empty string, and a
			// workflow that does so is green today.
			name:   "no min age",
			values: with("min-age-hours", ""),
			want:   wantWith(func(in *inputs) { in.options.MinAge = 0 }),
		},
		{
			// The failure this is here to prevent is the quiet one: an empty
			// token pruning nothing and leaving the run green.
			name:    "no token",
			values:  with("token", ""),
			wantErr: "token input is empty",
		},
		{
			name:    "a token that is only whitespace",
			values:  with("token", "  \n\t "),
			wantErr: "token input is empty",
		},
		{name: "no owner", values: with("owner", ""), wantErr: "owner input is empty"},
		{name: "no package", values: with("package", ""), wantErr: "package input is empty"},
		{name: "an unspellable dry run", values: with("dry-run", "yes"), wantErr: "dry-run"},
		{name: "a negative min age", values: with("min-age-hours", "-1"), wantErr: "min-age-hours"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setInputs(t, test.values)

			got, err := readInputs()
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("readInputs() = %+v, nil, want an error", got)
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Errorf("readInputs() error = %q, want it to mention %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readInputs(): %v", err)
			}
			if got != test.want {
				t.Errorf("readInputs() = %+v, want %+v", got, test.want)
			}
		})
	}
}

// recent builds a container version, tagged or not, updated an hour ago.
//
// Not `version`: internal/prune's tests have a helper by that name taking an
// age, and two helpers with one name and different signatures is a trap for
// whoever reads both files.
func recent(name string, id int64, tags []string) api.ContainerVersion {
	return api.ContainerVersion{
		ID:        id,
		Name:      name,
		UpdatedAt: time.Now().Add(-time.Hour),
		Tags:      tags,
	}
}

// fakeAPI is the packages API as prune sees it.
type fakeAPI struct {
	versions  []api.ContainerVersion
	deleteErr map[int64]error
	loginErr  error
}

func (f *fakeAPI) AuthenticatedLogin(context.Context) (string, error) {
	return "henrytill", f.loginErr
}

func (f *fakeAPI) ListVersions(context.Context, api.Target) ([]api.ContainerVersion, error) {
	return f.versions, nil
}

func (f *fakeAPI) DeleteVersion(_ context.Context, _ api.Target, id int64) error {
	return f.deleteErr[id]
}

// fakeRegistry reports no children, so every untagged version is unreferenced.
type fakeRegistry struct{}

func (fakeRegistry) ReadManifest(context.Context, string) ([]string, error) { return nil, nil }

func options() prune.Options {
	return prune.Options{Owner: "henrytill", PackageName: "devcontainer-debian"}
}

type discard struct{}

func (discard) Info(string)    {}
func (discard) Warning(string) {}
func (discard) Error(string)   {}

// report runs pruneAndReport with path as $GITHUB_OUTPUT.
//
// It does not read the file back: what a case wants from it differs, and one
// helper that both runs the code and decides whether the file exists hides that
// difference behind a nil map. actionstest.Outputs reads.
func report(t *testing.T, path string, options prune.Options, versions *fakeAPI) error {
	t.Helper()
	t.Setenv("GITHUB_OUTPUT", path)
	return pruneAndReport(context.Background(), options, versions, fakeRegistry{}, discard{})
}

func outputPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "output")
}

func TestPruneAndReportWritesEveryCount(t *testing.T) {
	// One tagged, two untagged and unreferenced.
	versions := &fakeAPI{versions: []api.ContainerVersion{
		recent("sha256:aaa", 1, []string{"latest"}),
		recent("sha256:bbb", 2, nil),
		recent("sha256:ccc", 3, nil),
	}}

	path := outputPath(t)
	if err := report(t, path, options(), versions); err != nil {
		t.Fatalf("pruneAndReport: %v", err)
	}

	want := map[string]string{"total": "3", "deleted": "2", "kept": "1", "failed": "0"}
	if got := actionstest.Outputs(t, path); !maps.Equal(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}
}

func TestPruneAndReportCountsADryRunWithoutDeleting(t *testing.T) {
	versions := &fakeAPI{versions: []api.ContainerVersion{
		recent("sha256:aaa", 1, []string{"latest"}),
		recent("sha256:bbb", 2, nil),
	}}

	dryRun := options()
	dryRun.DryRun = true

	path := outputPath(t)
	if err := report(t, path, dryRun, versions); err != nil {
		t.Fatalf("pruneAndReport: %v", err)
	}

	// deleted stays zero whatever a dry run found, which is why total is
	// reported at all: total minus kept is what a real run would remove.
	want := map[string]string{"total": "2", "deleted": "0", "kept": "1", "failed": "0"}
	if got := actionstest.Outputs(t, path); !maps.Equal(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}
}

func TestPruneAndReportWritesTheCountsBeforeFailing(t *testing.T) {
	versions := &fakeAPI{
		versions: []api.ContainerVersion{
			recent("sha256:bbb", 2, nil),
			recent("sha256:ccc", 3, nil),
		},
		deleteErr: map[int64]error{3: errors.New("403")},
	}

	path := outputPath(t)
	if err := report(t, path, options(), versions); err == nil {
		t.Fatal("pruneAndReport returned nil, want the delete failure")
	}

	// The counts have to be there despite the error: a workflow step reading
	// `deleted` after a failed run gets what actually happened.
	want := map[string]string{"total": "2", "deleted": "1", "kept": "0", "failed": "1"}
	if got := actionstest.Outputs(t, path); !maps.Equal(got, want) {
		t.Errorf("outputs = %v, want %v", got, want)
	}
}

func TestPruneAndReportWritesNothingWhenThePruneFails(t *testing.T) {
	versions := &fakeAPI{loginErr: errors.New("401")}

	path := outputPath(t)
	if err := report(t, path, options(), versions); err == nil {
		t.Fatal("pruneAndReport returned nil, want the API failure")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		// The raw bytes rather than actionstest.Outputs, which fails the test
		// itself on anything it cannot parse -- a partial write would then be
		// reported as a malformed heredoc rather than as the file existing at
		// all, which is what this case is about.
		//
		// #nosec G304 -- the path is this test's own t.TempDir().
		contents, _ := os.ReadFile(path)
		t.Errorf("$GITHUB_OUTPUT exists after a failed prune:\n%s", contents)
	}
}

// The failure itself belongs to actions.SetOutput and is tested there. What is
// tested here is that pruneAndReport returns it rather than reporting a clean
// run whose outputs never arrived.
func TestPruneAndReportFailsWhenGithubOutputIsUnset(t *testing.T) {
	versions := &fakeAPI{versions: []api.ContainerVersion{recent("sha256:aaa", 1, nil)}}

	if err := report(t, "", options(), versions); err == nil {
		t.Fatal("pruneAndReport returned nil, want the missing GITHUB_OUTPUT")
	}
}
