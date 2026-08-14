package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNextPageURL(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{"no header", "", ""},
		{"no next relation", `<https://api/x?page=1>; rel="prev"`, ""},
		{
			"next among others",
			`<https://api/x?page=3>; rel="next", <https://api/x?page=9>; rel="last"`,
			"https://api/x?page=3",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NextPageURL(test.link); got != test.want {
				t.Errorf("NextPageURL(%q) = %q, want %q", test.link, got, test.want)
			}
		})
	}
}

func TestTagsIsEmptyWhenMetadataIsMissing(t *testing.T) {
	var version ContainerVersion
	if err := json.Unmarshal([]byte(`{"id":1,"name":"sha256:a"}`), &version); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := version.Tags(); len(got) != 0 {
		t.Errorf("Tags() = %v, want empty", got)
	}
}

// TestParsesARealPayload uses a response captured from the packages API rather
// than a hand-written fixture, because time.Parse on updated_at is stricter
// than the Date.parse it replaces.
func TestParsesARealPayload(t *testing.T) {
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

	var versions []ContainerVersion
	if err := json.Unmarshal([]byte(payload), &versions); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(versions))
	}
	if got := versions[0].Tags(); len(got) != 1 || got[0] != "latest" {
		t.Errorf("Tags() = %v, want [latest]", got)
	}
	if len(versions[1].Tags()) != 0 {
		t.Errorf("Tags() = %v, want empty", versions[1].Tags())
	}
	if versions[0].UpdatedAt != "2024-11-03T18:22:42Z" {
		t.Errorf("UpdatedAt = %q", versions[0].UpdatedAt)
	}
}

func TestSendsTheTokenAndAPIVersionHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"login":"henrytill"}`))
	}))
	defer server.Close()

	login, err := NewClient("tok", server.URL, nil).AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedLogin: %v", err)
	}

	if login != "henrytill" {
		t.Errorf("login = %q, want henrytill", login)
	}
	if want := "Bearer tok"; got.Get("authorization") != want {
		t.Errorf("authorization = %q, want %q", got.Get("authorization"), want)
	}
	if got.Get("x-github-api-version") != APIVersion {
		t.Errorf("x-github-api-version = %q, want %q", got.Get("x-github-api-version"), APIVersion)
	}
}

func TestFollowsPaginationToTheLastPage(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`[{"id":2,"name":"sha256:b"}]`))
			return
		}
		w.Header().Set("link", `<`+serverURL(r)+`/next?page=2>; rel="next"`)
		w.Write([]byte(`[{"id":1,"name":"sha256:a"}]`))
	}))
	defer server.Close()

	versions, err := NewClient("tok", server.URL, nil).
		ListVersions(context.Background(), "/user/packages/container/p")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}

	if len(versions) != 2 || versions[0].ID != 1 || versions[1].ID != 2 {
		t.Fatalf("versions = %+v, want ids 1 and 2", versions)
	}
	want := "/user/packages/container/p/versions?per_page=100"
	if paths[0] != want {
		t.Errorf("first request = %q, want %q", paths[0], want)
	}
	if paths[1] != "/next?page=2" {
		t.Errorf("second request = %q, want /next?page=2", paths[1])
	}
}

// serverURL reconstructs the test server's base URL from a request.
func serverURL(r *http.Request) string { return "http://" + r.Host }

func TestFailsOnAnErrorStatusIncludingTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"Forbidden"}`))
	}))
	defer server.Close()

	_, err := NewClient("tok", server.URL, nil).AuthenticatedLogin(context.Background())

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("error = %q, want it to mention 403 and Forbidden", err)
	}
}

func TestDeletesAVersionByID(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := NewClient("tok", server.URL, nil).
		DeleteVersion(context.Background(), "/user/packages/container/p", 7)
	if err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}

	if method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", method)
	}
	if want := "/user/packages/container/p/versions/7"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestHonorsABaseURLWithATrailingSlashForEnterprise(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Write([]byte(`{"login":"henrytill"}`))
	}))
	defer server.Close()

	_, err := NewClient("tok", server.URL+"/api/v3/", nil).AuthenticatedLogin(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedLogin: %v", err)
	}

	if want := "/api/v3/user"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestDoesNotRetryAPermanentFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := NewClient("tok", server.URL, nil).AuthenticatedLogin(context.Background())

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1: a 404 must not be retried", calls)
	}
}
