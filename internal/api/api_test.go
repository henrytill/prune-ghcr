package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v90/github"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

// testPrefix is what WithEnterpriseURLs appends to a base URL whose host does
// not begin with "api.". Production is unaffected -- api.github.com is exempt
// by that rule, and a GHES GITHUB_API_URL already ends in /api/v3 -- but a
// test server on 127.0.0.1 gets the suffix, so the assertions carry it.
const testPrefix = "/api/v3"

// newTestClient points a client at a test server.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient("tok", server.URL+"/", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestConvertReadsTagsOutOfRawMetadata(t *testing.T) {
	// Metadata is json.RawMessage on PackageVersion, because the same field is
	// an array on webhook payloads, so the tags need decoding by hand.
	version := &github.PackageVersion{
		ID:       github.Ptr(int64(1)),
		Name:     github.Ptr("sha256:a"),
		Metadata: json.RawMessage(`{"package_type":"container","container":{"tags":["latest"]}}`),
	}

	converted, err := convert(version)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if len(converted.Tags) != 1 || converted.Tags[0] != "latest" {
		t.Errorf("Tags = %v, want [latest]", converted.Tags)
	}
	if converted.ID != 1 || converted.Name != "sha256:a" {
		t.Errorf("converted = %+v, want id 1 and name sha256:a", converted)
	}
}

func TestConvertTreatsAbsentMetadataAndTimestampAsEmpty(t *testing.T) {
	converted, err := convert(&github.PackageVersion{ID: github.Ptr(int64(1))})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	if len(converted.Tags) != 0 {
		t.Errorf("Tags = %v, want empty", converted.Tags)
	}
	// The zero time means unknown, which prune must not read as very old.
	if !converted.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt = %v, want the zero time", converted.UpdatedAt)
	}
}

// TestConvertParsesARealPayload uses a response captured from the packages API
// rather than a hand-written fixture, because go-github's Timestamp is stricter
// than the Date.parse it replaces.
func TestConvertParsesARealPayload(t *testing.T) {
	const payload = `[
	  {
	    "id": 296293181,
	    "name": "sha256:c9f6b8e6b1f9f1a1f0e0b3d9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3",
	    "url": "https://api.github.com/users/henrytill/packages/container/devcontainer-debian/versions/296293181",
	    "package_html_url": "https://github.com/users/henrytill/packages/container/package/devcontainer-debian",
	    "created_at": "2024-11-03T18:22:41Z",
	    "updated_at": "2024-11-03T18:22:42Z",
	    "html_url": "https://github.com/users/henrytill/packages/container/devcontainer-debian/296293181",
	    "metadata": { "package_type": "container", "container": { "tags": ["latest"] } }
	  },
	  {
	    "id": 296293180,
	    "name": "sha256:11223344556677889900aabbccddeeff00112233445566778899aabbccddeeff",
	    "created_at": "2024-11-03T18:22:40Z",
	    "updated_at": "2024-11-03T18:22:40Z",
	    "metadata": { "package_type": "container", "container": { "tags": [] } }
	  }
	]`

	var raw []*github.PackageVersion
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d versions, want 2", len(raw))
	}

	first, err := convert(raw[0])
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got := first.Tags; len(got) != 1 || got[0] != "latest" {
		t.Errorf("Tags = %v, want [latest]", got)
	}
	if want := "2024-11-03T18:22:42Z"; first.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Errorf("UpdatedAt = %v, want %s", first.UpdatedAt, want)
	}

	second, err := convert(raw[1])
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(second.Tags) != 0 {
		t.Errorf("Tags = %v, want empty", second.Tags)
	}
}

func TestSendsTheToken(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"login":"henrytill"}`))
	}))
	defer server.Close()

	login, err := newTestClient(t, server).AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedLogin: %v", err)
	}

	if login != "henrytill" {
		t.Errorf("login = %q, want henrytill", login)
	}
	if want := "Bearer tok"; got.Get("authorization") != want {
		t.Errorf("authorization = %q, want %q", got.Get("authorization"), want)
	}
}

// TestUsesTheUserPathWhenTheTokenOwnsThePackage guards the reason the
// user/organization split exists: /user is the only path that can delete
// versions of a user-owned package.
func TestUsesTheUserPathWhenTheTokenOwnsThePackage(t *testing.T) {
	var listed, deleted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		listed = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	target := Target{Owner: "henrytill", PackageName: "p", UserOwned: true}

	if _, err := client.ListVersions(context.Background(), target); err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if err := client.DeleteVersion(context.Background(), target, 7); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}

	if want := testPrefix + "/user/packages/container/p/versions"; listed != want {
		t.Errorf("listed %q, want %q", listed, want)
	}
	if want := testPrefix + "/user/packages/container/p/versions/7"; deleted != want {
		t.Errorf("deleted %q, want %q", deleted, want)
	}
}

func TestUsesTheOrgsPathForAPackageOwnedBySomeoneElse(t *testing.T) {
	var listed, deleted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		listed = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	target := Target{Owner: "some-org", PackageName: "p"}

	if _, err := client.ListVersions(context.Background(), target); err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if err := client.DeleteVersion(context.Background(), target, 7); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}

	if want := testPrefix + "/orgs/some-org/packages/container/p/versions"; listed != want {
		t.Errorf("listed %q, want %q", listed, want)
	}
	if want := testPrefix + "/orgs/some-org/packages/container/p/versions/7"; deleted != want {
		t.Errorf("deleted %q, want %q", deleted, want)
	}
}

func TestFollowsPaginationToTheLastPage(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id":2,"name":"sha256:b"}]`))
			return
		}
		w.Header().Set("Link", `<`+"http://"+r.Host+r.URL.Path+`?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id":1,"name":"sha256:a"}]`))
	}))
	defer server.Close()

	versions, err := newTestClient(t, server).ListVersions(context.Background(),
		Target{Owner: "henrytill", PackageName: "p", UserOwned: true})
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}

	if len(versions) != 2 || versions[0].ID != 1 || versions[1].ID != 2 {
		t.Fatalf("versions = %+v, want ids 1 and 2", versions)
	}
	if len(pages) != 2 || pages[1] != "2" {
		t.Errorf("requested pages = %v, want a second page", pages)
	}
}

func TestFailsOnAnErrorStatusIncludingTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer server.Close()

	_, err := newTestClient(t, server).AuthenticatedLogin(context.Background())

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("error = %q, want it to mention 403 and Forbidden", err)
	}
}

// TestRetriesARateLimit guards the carve-out in statusError: a rate limit
// answers 403 like a permissions failure, but waiting fixes it, so it must
// stay retryable.
func TestRetriesARateLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			// A reset already in the past keeps go-github's client-side
			// limiter from short-circuiting the second request.
			w.Header().Set("X-RateLimit-Limit", "60")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset",
				strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"henrytill"}`))
	}))
	defer server.Close()

	login, err := newTestClient(t, server).AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedLogin: %v", err)
	}

	if login != "henrytill" {
		t.Errorf("login = %q, want henrytill", login)
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2: a rate limit must be retried", calls)
	}
}

func TestDoesNotRetryAPermanentFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).AuthenticatedLogin(context.Background())

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1: a 404 must not be retried", calls)
	}
}

func TestRetriesATransientFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"login":"henrytill"}`))
	}))
	defer server.Close()

	var warnings []string
	client, err := NewClient("tok", server.URL+"/",
		func(message string) { warnings = append(warnings, message) })
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	login, err := client.AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedLogin: %v", err)
	}

	if login != "henrytill" {
		t.Errorf("login = %q, want henrytill", login)
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v, want one", warnings)
	}
}

// TestMain turns the retry backoff off so the retry test does not sleep.
func TestMain(m *testing.M) {
	retry.DefaultBaseDelay = 0
	os.Exit(m.Run())
}
