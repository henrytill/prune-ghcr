// Package registry reads image manifests from a container registry, which is
// how the action learns which untagged versions a tagged index still
// references.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

// Host is the registry the action prunes.
const Host = "ghcr.io"

// Manifest is an image manifest. Manifests is populated on a multi-arch index.
type Manifest struct {
	Manifests []Descriptor
}

// Descriptor points at a manifest under an index.
type Descriptor struct {
	Digest string
}

// Client reads manifests from one repository. It is not safe for concurrent
// use: a failed read replaces the cached authentication.
type Client struct {
	repository name.Repository
	options    []remote.Option
	puller     *remote.Puller
	backoff    retry.Backoff
}

// NewClient returns a client authorized to read a repository's manifests.
//
// registryHost is injectable so the tests can point it at an httptest server;
// an empty value selects ghcr.io. Unlike the hand-rolled client this replaced,
// no token is exchanged here: go-containerregistry performs the exchange lazily
// on the first request, so a bad token surfaces at the first manifest read
// rather than at construction.
func NewClient(
	registryHost, owner, packageName, githubToken string, warn func(string),
) (*Client, error) {
	if registryHost == "" {
		registryHost = Host
	}

	// Registry paths are lowercase, even when the GitHub owner or package name
	// is not.
	path := fmt.Sprintf("%s/%s/%s", registryHost,
		strings.ToLower(owner), strings.ToLower(packageName))

	options := []name.Option{}
	if isLoopback(registryHost) {
		options = append(options, name.Insecure)
	}

	repository, err := name.NewRepository(path, options...)
	if err != nil {
		return nil, fmt.Errorf("parsing repository %q: %w", path, err)
	}

	remoteOptions := []remote.Option{
		remote.WithAuth(&authn.Basic{Username: owner, Password: githubToken}),
		// One attempt: go-containerregistry otherwise retries transient
		// failures itself, three times with its own backoff, invisibly --
		// its retry log is discarded by default. Stacked under retry.Do
		// that means up to nine requests per read, so retrying is left to
		// internal/retry alone, which also warns per retry.
		remote.WithRetryBackoff(remote.Backoff{Steps: 1}),
	}

	// One puller for the client's lifetime, so the ping and token exchange
	// happen once per run rather than once per manifest.
	puller, err := remote.NewPuller(remoteOptions...)
	if err != nil {
		return nil, fmt.Errorf("configuring the registry client: %w", err)
	}

	return &Client{
		repository: repository,
		options:    remoteOptions,
		puller:     puller,
		backoff:    retry.NewBackoff(warn),
	}, nil
}

// resetPuller discards the cached authentication after a failed read. The
// puller remembers its first handshake, a failed one included, so a retry
// against the old puller would replay the failure from the cache instead of
// reaching the network. NewPuller only fails on invalid options, which
// NewClient already validated; if it fails anyway the old puller stays, which
// at worst replays the error the caller is already seeing.
func (c *Client) resetPuller() {
	if puller, err := remote.NewPuller(c.options...); err == nil {
		c.puller = puller
	}
}

// isLoopback reports whether host is an httptest server rather than a real
// registry, which is the only case that speaks plain HTTP.
func isLoopback(host string) bool {
	return strings.HasPrefix(host, "127.0.0.1:") ||
		strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "[::1]:")
}

// labelWidth truncates a digest for log lines: "sha256:" plus twelve hex
// characters, the abbreviation docker itself prints.
const labelWidth = len("sha256:") + 12

// ReadManifest fetches a manifest by digest.
//
// The puller negotiates the media types itself, so the explicit Accept list the
// hand-rolled client carried is no longer spelled out here; a single-platform
// manifest simply comes back with no children.
func (c *Client) ReadManifest(ctx context.Context, reference string) (Manifest, error) {
	label := reference
	if len(label) > labelWidth {
		label = label[:labelWidth]
	}

	return retry.Do(ctx, func(ctx context.Context) (Manifest, error) {
		var manifest Manifest

		descriptor, err := c.puller.Get(ctx, c.repository.Digest(reference))
		if err != nil {
			c.resetPuller()
			return manifest, classify(err)
		}

		if !descriptor.MediaType.IsIndex() {
			return manifest, nil
		}

		index, err := descriptor.ImageIndex()
		if err != nil {
			return manifest, classify(err)
		}
		indexManifest, err := index.IndexManifest()
		if err != nil {
			return manifest, classify(err)
		}

		for _, child := range indexManifest.Manifests {
			manifest.Manifests = append(manifest.Manifests,
				Descriptor{Digest: child.Digest.String()})
		}
		return manifest, nil
	}, c.backoff.Options("manifest "+label))
}

// classify marks a registry error retryable or not by its HTTP status, so a 403
// or 404 fails immediately instead of burning the backoff.
func classify(err error) error {
	var transportErr *transport.Error
	if errors.As(err, &transportErr) {
		return retry.StatusError(err.Error(), transportErr.StatusCode)
	}
	return err
}
