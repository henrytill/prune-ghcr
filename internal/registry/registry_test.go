package registry

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

// tokenServer answers the token endpoint with token, and hands every other
// request to handler.
func tokenServer(t *testing.T, token string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"token":"` + token + `"}`))
			return
		}
		if handler != nil {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestExchangesTheGitHubTokenForAPullScopedRegistryToken(t *testing.T) {
	var query, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, authorization = r.URL.RawQuery, r.Header.Get("authorization")
		w.Write([]byte(`{"token":"registry-token"}`))
	}))
	defer server.Close()

	_, err := NewClient(context.Background(), server.URL,
		"HenryTill", "Devcontainer-Debian", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Registry repository paths are lowercase even when the package name is not.
	if want := "repository%3Ahenrytill%2Fdevcontainer-debian%3Apull"; !strings.Contains(query, want) {
		t.Errorf("query = %q, want it to contain %q", query, want)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("HenryTill:ghp_tok"))
	if authorization != want {
		t.Errorf("authorization = %q, want %q", authorization, want)
	}
}

func TestFailsWhenTheTokenResponseCarriesNoToken(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err := NewClient(context.Background(), server.URL, "henrytill", "p", "ghp_tok", nil)

	if err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("error = %v, want one mentioning no token", err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1: a missing token is not retryable", calls)
	}
}

func TestRequestsManifestsWithEveryIndexMediaTypeAccepted(t *testing.T) {
	var path, accept, authorization string
	server := tokenServer(t, "registry-token", func(w http.ResponseWriter, r *http.Request) {
		path, accept = r.URL.Path, r.Header.Get("accept")
		authorization = r.Header.Get("authorization")
		w.Write([]byte(`{"manifests":[{"digest":"sha256:child"}]}`))
	})

	client, err := NewClient(context.Background(), server.URL, "henrytill", "p", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	manifest, err := client.ReadManifest(context.Background(), "sha256:index")
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if len(manifest.Manifests) != 1 || manifest.Manifests[0].Digest != "sha256:child" {
		t.Errorf("manifest = %+v, want one child sha256:child", manifest)
	}
	if want := "/v2/henrytill/p/manifests/sha256:index"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if authorization != "Bearer registry-token" {
		t.Errorf("authorization = %q, want Bearer registry-token", authorization)
	}
	for _, mediaType := range []string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
	} {
		if !strings.Contains(accept, mediaType) {
			t.Errorf("accept = %q, want it to contain %q", accept, mediaType)
		}
	}
}

func TestFailsOnAnErrorStatus(t *testing.T) {
	server := tokenServer(t, "registry-token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[]}`))
	})

	client, err := NewClient(context.Background(), server.URL, "henrytill", "p", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ReadManifest(context.Background(), "sha256:missing")

	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want one mentioning 404", err)
	}
}

func TestRetriesATransientTokenFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"token":"registry-token"}`))
	}))
	defer server.Close()

	var warnings []string
	client, err := NewClient(context.Background(), server.URL, "henrytill", "p", "ghp_tok",
		func(message string) { warnings = append(warnings, message) })
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.token != "registry-token" {
		t.Errorf("token = %q, want registry-token", client.token)
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
