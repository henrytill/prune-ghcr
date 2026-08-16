package prune

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/henrytill/prune-ghcr/internal/api"
	"github.com/henrytill/prune-ghcr/internal/registry"
)

// version builds a container version, optionally tagged and aged.
func version(name string, id int64, tags []string, age time.Duration) api.ContainerVersion {
	return api.ContainerVersion{
		ID:        id,
		Name:      name,
		UpdatedAt: time.Now().Add(-age),
		Tags:      tags,
	}
}

type fakeAPI struct {
	login     string
	versions  []api.ContainerVersion
	deleteErr map[int64]error

	listedTargets  []api.Target
	deletedIDs     []int64
	deletedTargets []api.Target
}

func (f *fakeAPI) AuthenticatedLogin(context.Context) (string, error) {
	return f.login, nil
}

func (f *fakeAPI) ListVersions(_ context.Context, target api.Target) ([]api.ContainerVersion, error) {
	f.listedTargets = append(f.listedTargets, target)
	return f.versions, nil
}

func (f *fakeAPI) DeleteVersion(_ context.Context, target api.Target, id int64) error {
	f.deletedTargets = append(f.deletedTargets, target)
	f.deletedIDs = append(f.deletedIDs, id)
	return f.deleteErr[id]
}

type fakeRegistry struct {
	manifests map[string]registry.Manifest
	err       error

	read []string
}

func (f *fakeRegistry) ReadManifest(_ context.Context, reference string) (registry.Manifest, error) {
	f.read = append(f.read, reference)
	if f.err != nil {
		return registry.Manifest{}, f.err
	}
	return f.manifests[reference], nil
}

type recorder struct {
	infos    []string
	warnings []string
	errors   []string
}

func (r *recorder) Info(message string)    { r.infos = append(r.infos, message) }
func (r *recorder) Warning(message string) { r.warnings = append(r.warnings, message) }
func (r *recorder) Error(message string)   { r.errors = append(r.errors, message) }

func contains(messages []string, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}

func options() Options {
	return Options{Owner: "henrytill", PackageName: "devcontainer-debian"}
}

// run drives Prune with the given collaborators and fails the test on error.
func run(t *testing.T, opts Options, versions *fakeAPI, manifests *fakeRegistry) (Result, *recorder) {
	t.Helper()
	log := &recorder{}
	result, err := Prune(context.Background(), opts, versions, manifests, log)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	return result, log
}

func TestUsesUserPathWhenTokenOwnsPackage(t *testing.T) {
	versions := &fakeAPI{
		login:    "henrytill",
		versions: []api.ContainerVersion{version("sha256:aa", 1, nil, 0)},
	}

	run(t, options(), versions, &fakeRegistry{})

	want := api.Target{Owner: "henrytill", PackageName: "devcontainer-debian", UserOwned: true}
	if got := versions.listedTargets; len(got) != 1 || got[0] != want {
		t.Errorf("listed targets = %v, want [%+v]", got, want)
	}
	if got := versions.deletedTargets; len(got) != 1 || got[0] != want {
		t.Errorf("deleted targets = %v, want [%+v]", got, want)
	}
}

func TestUsesOrgsPathForSomeoneElsesPackage(t *testing.T) {
	versions := &fakeAPI{login: "someone-else"}

	run(t, options(), versions, &fakeRegistry{})

	want := api.Target{Owner: "henrytill", PackageName: "devcontainer-debian"}
	if got := versions.listedTargets; len(got) != 1 || got[0] != want {
		t.Errorf("listed targets = %v, want [%+v]", got, want)
	}
}

func TestKeepsTaggedVersionsAndChildrenOfATaggedIndex(t *testing.T) {
	versions := &fakeAPI{
		login: "henrytill",
		versions: []api.ContainerVersion{
			version("sha256:index", 1, []string{"latest"}, 0),
			version("sha256:amd64", 2, nil, 0),
			version("sha256:arm64", 3, nil, 0),
			version("sha256:orphan", 4, nil, 0),
		},
	}
	manifests := &fakeRegistry{manifests: map[string]registry.Manifest{
		"sha256:index": {Manifests: []registry.Descriptor{
			{Digest: "sha256:amd64"}, {Digest: "sha256:arm64"},
		}},
	}}

	result, _ := run(t, options(), versions, manifests)

	if got := manifests.read; len(got) != 1 || got[0] != "sha256:index" {
		t.Errorf("read manifests = %v, want [sha256:index]", got)
	}
	if got := versions.deletedIDs; len(got) != 1 || got[0] != 4 {
		t.Errorf("deleted ids = %v, want [4]", got)
	}
	want := Result{Total: 4, Kept: 3, Deleted: 1}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestRefusesToPruneWhenATaggedManifestCannotBeRead(t *testing.T) {
	versions := &fakeAPI{
		login: "henrytill",
		versions: []api.ContainerVersion{
			version("sha256:index", 1, []string{"latest"}, 0),
			version("sha256:orphan", 2, nil, 0),
		},
	}
	manifests := &fakeRegistry{err: errors.New("502 Bad Gateway")}

	_, err := Prune(context.Background(), options(), versions, manifests, &recorder{})

	if err == nil || !strings.Contains(err.Error(), "could not read sha256:index") {
		t.Fatalf("error = %v, want one mentioning could not read sha256:index", err)
	}
	if len(versions.deletedIDs) != 0 {
		t.Errorf("deleted %v, want nothing", versions.deletedIDs)
	}
}

func TestSkipsVersionsYoungerThanMinAge(t *testing.T) {
	versions := &fakeAPI{
		login: "henrytill",
		versions: []api.ContainerVersion{
			version("sha256:fresh", 1, nil, 30*time.Minute),
			version("sha256:stale", 2, nil, 5*time.Hour),
		},
	}

	opts := options()
	opts.MinAge = time.Hour
	result, _ := run(t, opts, versions, &fakeRegistry{})

	if got := versions.deletedIDs; len(got) != 1 || got[0] != 2 {
		t.Errorf("deleted ids = %v, want [2]", got)
	}
	want := Result{Total: 2, Kept: 1, Deleted: 1}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestSkipsVersionsWithNoUsableTimestamp(t *testing.T) {
	broken := version("sha256:broken", 1, nil, 0)
	broken.UpdatedAt = time.Time{}
	versions := &fakeAPI{
		login:    "henrytill",
		versions: []api.ContainerVersion{broken, version("sha256:fine", 2, nil, 0)},
	}

	result, log := run(t, options(), versions, &fakeRegistry{})

	if got := versions.deletedIDs; len(got) != 1 || got[0] != 2 {
		t.Errorf("deleted ids = %v, want [2]", got)
	}
	// A skip the run survives is a warning; an error annotation would paint
	// a successful run red.
	if !contains(log.warnings, "sha256:broken") {
		t.Errorf("warnings = %v, want one mentioning sha256:broken", log.warnings)
	}
	want := Result{Total: 2, Kept: 1, Deleted: 1}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestDeletesNothingInDryRun(t *testing.T) {
	versions := &fakeAPI{
		login:    "henrytill",
		versions: []api.ContainerVersion{version("sha256:orphan", 1, nil, 0)},
	}

	opts := options()
	opts.DryRun = true
	result, _ := run(t, opts, versions, &fakeRegistry{})

	if len(versions.deletedIDs) != 0 {
		t.Errorf("deleted %v, want nothing", versions.deletedIDs)
	}
	want := Result{Total: 1, Kept: 0}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestReportsNothingToPruneWhenEveryVersionIsReferenced(t *testing.T) {
	versions := &fakeAPI{
		login:    "henrytill",
		versions: []api.ContainerVersion{version("sha256:index", 1, []string{"latest"}, 0)},
	}

	result, _ := run(t, options(), versions, &fakeRegistry{})

	if len(versions.deletedIDs) != 0 {
		t.Errorf("deleted %v, want nothing", versions.deletedIDs)
	}
	want := Result{Total: 1, Kept: 1}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestCountsDeleteFailuresAndContinues(t *testing.T) {
	versions := &fakeAPI{
		login: "henrytill",
		versions: []api.ContainerVersion{
			version("sha256:a", 1, nil, 0),
			version("sha256:b", 2, nil, 0),
		},
		deleteErr: map[int64]error{1: errors.New("403 Forbidden")},
	}

	result, log := run(t, options(), versions, &fakeRegistry{})

	if len(versions.deletedIDs) != 2 {
		t.Errorf("attempted %v, want both", versions.deletedIDs)
	}
	if !contains(log.errors, "failed to delete sha256:a") {
		t.Errorf("errors = %v, want one mentioning failed to delete sha256:a", log.errors)
	}
	want := Result{Total: 2, Kept: 0, Deleted: 1, Failed: 1}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}
