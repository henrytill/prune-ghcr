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

// Client reads manifests from one repository.
type Client struct {
	repository name.Repository
	options    []remote.Option
	warn       func(string)
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

	return &Client{
		repository: repository,
		options: []remote.Option{
			remote.WithAuth(&authn.Basic{Username: owner, Password: githubToken}),
		},
		warn: warn,
	}, nil
}

// isLoopback reports whether host is an httptest server rather than a real
// registry, which is the only case that speaks plain HTTP.
func isLoopback(host string) bool {
	return strings.HasPrefix(host, "127.0.0.1:") ||
		strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "[::1]:")
}

// ReadManifest fetches a manifest by digest or tag.
//
// remote.Get negotiates the media types itself, so the explicit Accept list the
// hand-rolled client carried is no longer spelled out here; a single-platform
// manifest simply comes back with no children.
func (c *Client) ReadManifest(ctx context.Context, reference string) (Manifest, error) {
	label := reference
	if len(label) > 19 {
		label = label[:19]
	}

	return retry.Do(ctx, func(ctx context.Context) (Manifest, error) {
		var manifest Manifest

		descriptor, err := remote.Get(c.repository.Digest(reference),
			append(c.options, remote.WithContext(ctx))...)
		if err != nil {
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
	}, retry.New("manifest "+label, c.warn))
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
