package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

const (
	indexMediaType    = "application/vnd.oci.image.index.v1+json"
	manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
)

// childlessManifest is a single-platform manifest, which most of these tests
// only need the registry to return something valid for.
const childlessManifest = `{"schemaVersion":2,"mediaType":"` + manifestMediaType + `"}`

// serveManifest answers every manifest request with one body.
func serveManifest(mediaType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		_, _ = w.Write([]byte(body))
	}
}

// registryServer stands in for ghcr.io. It answers the /v2/ ping and the token
// endpoint that go-containerregistry negotiates before any manifest read, and
// hands manifest requests to handler.
func registryServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return registryServerWithPing(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, handler)
}

// registryServerWithPing is registryServer with the handshake observable: the
// /v2/ ping goes to pingHandler instead of unconditionally succeeding.
func registryServerWithPing(t *testing.T, pingHandler, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/" || r.URL.Path == "/v2":
			pingHandler(w, r)
		case strings.HasPrefix(r.URL.Path, "/token"):
			_, _ = w.Write([]byte(`{"token":"registry-token"}`))
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

// newTestClient points a client at a test server -- over plain HTTP, since
// httptest serves that -- with the retry backoff turned off so a retry does not
// sleep.
func newTestClient(t *testing.T, server *httptest.Server, owner, packageName string) *Client {
	t.Helper()
	host := strings.TrimPrefix(server.URL, "http://")
	client, err := newClient(host, true, owner, packageName, "ghp_tok", nil)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	client.backoff.BaseDelay = 0
	return client
}

func TestLowercasesTheRepositoryPath(t *testing.T) {
	var path string
	serve := serveManifest(manifestMediaType, childlessManifest)
	server := registryServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		serve(w, r)
	})

	client := newTestClient(t, server, "HenryTill", "Devcontainer-Debian")
	if _, err := client.ReadManifest(context.Background(), digestOf(childlessManifest)); err != nil {
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

	server := registryServer(t, serveManifest(indexMediaType, index))

	client := newTestClient(t, server, "henrytill", "p")
	children, err := client.ReadManifest(context.Background(), digestOf(index))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if want := []string{child, other}; !slices.Equal(children, want) {
		t.Errorf("children = %v, want %v", children, want)
	}
}

// TestASinglePlatformManifestHasNoChildren covers the case the explicit Accept
// list used to guard: a non-index manifest must come back empty rather than
// erroring, so its version is simply kept.
func TestASinglePlatformManifestHasNoChildren(t *testing.T) {
	body := `{"schemaVersion":2,"mediaType":"` + manifestMediaType + `","layers":[]}`
	server := registryServer(t, serveManifest(manifestMediaType, body))

	client := newTestClient(t, server, "henrytill", "p")
	children, err := client.ReadManifest(context.Background(), digestOf(body))
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if len(children) != 0 {
		t.Errorf("children = %v, want none", children)
	}
}

func TestFailsOnAnErrorStatusWithoutRetrying(t *testing.T) {
	calls := 0
	server := registryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`))
	})

	client := newTestClient(t, server, "henrytill", "p")

	_, err := client.ReadManifest(context.Background(), "sha256:"+strings.Repeat("a", 64))

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if calls != 1 {
		t.Errorf("made %d manifest requests, want 1: a 404 must not be retried", calls)
	}
}

// TestRetriesATransientStatusOnlyThroughTheSharedBackoff guards the
// WithRetryBackoff(Steps: 1) option: go-containerregistry retries transient
// failures itself unless told not to, so without it a persistent 502 would be
// requested nine times -- three invisible transport retries under each of
// retry.Do's three attempts.
func TestRetriesATransientStatusOnlyThroughTheSharedBackoff(t *testing.T) {
	calls := 0
	server := registryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"errors":[{"code":"UNAVAILABLE"}]}`))
	})

	client := newTestClient(t, server, "henrytill", "p")

	_, err := client.ReadManifest(context.Background(), "sha256:"+strings.Repeat("a", 64))

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if want := retry.DefaultAttempts; calls != want {
		t.Errorf("made %d manifest requests, want %d: only retry.Do may retry", calls, want)
	}
}

// TestReusesTheAuthHandshakeAcrossReads guards the shared puller: the ping
// and token exchange happen once per client, not once per manifest.
func TestReusesTheAuthHandshakeAcrossReads(t *testing.T) {
	pings := 0
	server := registryServerWithPing(t, func(w http.ResponseWriter, _ *http.Request) {
		pings++
		w.WriteHeader(http.StatusOK)
	}, serveManifest(manifestMediaType, childlessManifest))

	client := newTestClient(t, server, "henrytill", "p")
	for range 2 {
		if _, err := client.ReadManifest(context.Background(), digestOf(childlessManifest)); err != nil {
			t.Fatalf("ReadManifest: %v", err)
		}
	}

	if pings != 1 {
		t.Errorf("made %d handshakes for 2 reads, want 1", pings)
	}
}

// TestRetriesAFailedAuthHandshake guards resetPuller: the puller caches a
// failed handshake, so without the reset every retry would replay the failure
// from the cache and never reach the network again.
func TestRetriesAFailedAuthHandshake(t *testing.T) {
	pings := 0
	server := registryServerWithPing(t, func(w http.ResponseWriter, _ *http.Request) {
		pings++
		if pings == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}, serveManifest(manifestMediaType, childlessManifest))

	client := newTestClient(t, server, "henrytill", "p")
	if _, err := client.ReadManifest(context.Background(), digestOf(childlessManifest)); err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if pings != 2 {
		t.Errorf("made %d handshakes, want 2: the cached failure must not be replayed", pings)
	}
}

// TestKeepsTheTransportErrorReachable guards the boundary classify sits on: a
// 404 for a manifest that never existed and one for a repository the token
// cannot see arrive as the same status, and only the typed error carries the
// registry's own error code.
func TestKeepsTheTransportErrorReachable(t *testing.T) {
	server := registryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`))
	})

	client := newTestClient(t, server, "henrytill", "p")

	_, err := client.ReadManifest(context.Background(), "sha256:"+strings.Repeat("a", 64))

	var transportErr *transport.Error
	if !errors.As(err, &transportErr) {
		t.Fatalf("error = %v, want the *transport.Error reachable", err)
	}
	if got := transportErr.StatusCode; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestRejectsAnUnparseableRepository(t *testing.T) {
	if _, err := NewClient("hen ry", "p", "tok", nil); err == nil {
		t.Error("NewClient = nil error, want a failure on an invalid repository")
	}
}
