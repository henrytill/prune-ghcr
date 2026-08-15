# Create Unit Test(s)

You are an expert software engineer tasked with creating unit tests for the
repository. Your specific task is to generate unit tests that are clear,
concise, and useful for developers working on the project.

## Guidelines

Ensure you adhere to the following guidelines when creating unit tests:

- Use a clear and consistent format for the unit tests
- Include a summary of the functionality being tested
- Use descriptive test names that clearly convey their purpose
- Ensure tests cover both the main path of success and edge cases
- Use proper assertions to validate the expected outcomes
- Use the standard library `testing` package; there is no mocking framework
- Place tests beside the code they test, in a `_test.go` file in the same
  package, so unexported helpers can be tested directly
- Drive HTTP clients with `httptest.NewServer` rather than by faking a transport
- Satisfy the interfaces declared in the consuming package with plain structs
  when a collaborator has to be faked
- State the reason in the assertion message when a test exists to guard an
  invariant, so a later reader knows what breaking it would cost

## Example

Use the following as an example of how to structure your unit tests:

```go
package prune

import (
	"context"
	"strings"
	"testing"
)

// fakeAPI records what it was asked to do, so a test can assert on the calls
// rather than on a mock framework's bookkeeping.
type fakeAPI struct {
	login    string
	versions []api.ContainerVersion

	deletedIDs []int64
}

func (f *fakeAPI) AuthenticatedLogin(context.Context) (string, error) {
	return f.login, nil
}

func (f *fakeAPI) ListVersions(
	context.Context, api.Target,
) ([]api.ContainerVersion, error) {
	return f.versions, nil
}

func (f *fakeAPI) DeleteVersion(
	_ context.Context, _ api.Target, id int64,
) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return nil
}

func TestKeepsTheChildrenOfATaggedIndex(t *testing.T) {
	versions := &fakeAPI{
		login: "henrytill",
		versions: []api.ContainerVersion{
			version("sha256:index", 1, []string{"latest"}, 0),
			version("sha256:child", 2, nil, 0),
		},
	}
	manifests := &fakeRegistry{manifests: map[string]registry.Manifest{
		"sha256:index": {Manifests: []registry.Descriptor{{Digest: "sha256:child"}}},
	}}

	result, err := Prune(
		context.Background(), options(), versions, manifests, &recorder{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Deleting a child of a tagged index breaks the live tag, which is the
	// whole reason the manifest walk exists.
	if len(versions.deletedIDs) != 0 {
		t.Errorf("deleted %v, want nothing", versions.deletedIDs)
	}
	if want := (Result{Total: 2, Kept: 2}); result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
}

func TestFailsWhenATaggedManifestCannotBeRead(t *testing.T) {
	versions := &fakeAPI{login: "henrytill", versions: []api.ContainerVersion{
		version("sha256:index", 1, []string{"latest"}, 0),
	}}
	manifests := &fakeRegistry{err: errors.New("502 Bad Gateway")}

	_, err := Prune(
		context.Background(), options(), versions, manifests, &recorder{})

	const want = "could not read sha256:index"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one naming the unreadable manifest", err)
	}
}
```
