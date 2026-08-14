package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

const (
	indexMediaType    = "application/vnd.oci.image.index.v1+json"
	manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
)

// registryServer stands in for ghcr.io. It answers the /v2/ ping and the token
// endpoint that go-containerregistry negotiates before any manifest read, and
// hands manifest requests to handler.
func registryServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/token"):
			w.Write([]byte(`{"token":"registry-token"}`))
		default:
			handler(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// digestOf is the digest go-containerregistry will require the body to have.
// Unlike the hand-rolled client this replaced, remote.Get verifies that the
// content actually hashes to the digest requested, so the tests cannot use a
// made-up one.
func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// host strips the scheme, since NewClient takes a registry host.
func host(server *httptest.Server) string {
	return strings.TrimPrefix(server.URL, "http://")
}

func TestLowercasesTheRepositoryPath(t *testing.T) {
	body := `{"schemaVersion":2,"mediaType":"` + manifestMediaType + `"}`
	var path string
	server := registryServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", manifestMediaType)
		w.Write([]byte(body))
	})

	client, err := NewClient(host(server), "HenryTill", "Devcontainer-Debian", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.ReadManifest(context.Background(), digestOf(body)); err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	// Registry paths are lowercase even when the owner and package are not.
	if want := "/v2/henrytill/devcontainer-debian/manifests/"; !strings.HasPrefix(path, want) {
		t.Errorf("path = %q, want it to start with %q", path, want)
	}
}

func TestReturnsTheChildrenOfAnIndex(t *testing.T) {
	child := "sha256:" + strings.Repeat("b", 64)
	other := "sha256:" + strings.Repeat("c", 64)
	index := `{"schemaVersion":2,"mediaType":"` + indexMediaType + `","manifests":[
		{"mediaType":"` + manifestMediaType + `","digest":"` + child + `","size":1},
		{"mediaType":"` + manifestMediaType + `","digest":"` + other + `","size":1}
	]}`

	server := registryServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", indexMediaType)
		w.Write([]byte(index))
	})

	client, err := NewClient(host(server), "henrytill", "p", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	manifest, err := client.ReadManifest(context.Background(), digestOf(index))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if len(manifest.Manifests) != 2 {
		t.Fatalf("got %d children, want 2: %+v", len(manifest.Manifests), manifest)
	}
	if manifest.Manifests[0].Digest != child || manifest.Manifests[1].Digest != other {
		t.Errorf("children = %+v, want %s and %s", manifest.Manifests, child, other)
	}
}

// TestASinglePlatformManifestHasNoChildren covers the case the explicit Accept
// list used to guard: a non-index manifest must come back empty rather than
// erroring, so its version is simply kept.
func TestASinglePlatformManifestHasNoChildren(t *testing.T) {
	body := `{"schemaVersion":2,"mediaType":"` + manifestMediaType + `","layers":[]}`
	server := registryServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", manifestMediaType)
		w.Write([]byte(body))
	})

	client, err := NewClient(host(server), "henrytill", "p", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	manifest, err := client.ReadManifest(context.Background(), digestOf(body))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if len(manifest.Manifests) != 0 {
		t.Errorf("children = %+v, want none", manifest.Manifests)
	}
}

func TestFailsOnAnErrorStatusWithoutRetrying(t *testing.T) {
	calls := 0
	server := registryServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`))
	})

	client, err := NewClient(host(server), "henrytill", "p", "ghp_tok", nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ReadManifest(context.Background(), "sha256:"+strings.Repeat("a", 64))

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if calls != 1 {
		t.Errorf("made %d manifest requests, want 1: a 404 must not be retried", calls)
	}
}

func TestRejectsAnUnparseableRepository(t *testing.T) {
	if _, err := NewClient("ghcr.io", "hen ry", "p", "tok", nil); err == nil {
		t.Error("NewClient = nil error, want a failure on an invalid repository")
	}
}

// TestMain turns the retry backoff off so a retry does not sleep.
func TestMain(m *testing.M) {
	retry.DefaultBaseDelay = 0
	os.Exit(m.Run())
}
