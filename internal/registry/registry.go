// Package registry reads image manifests from a container registry, which is
// how the action learns which untagged versions a tagged index still
// references.
//
// It goes through go-containerregistry rather than net/http for one reason
// above the others: remote.Get negotiates manifest media types itself, so there
// is no hand-maintained Accept list, and it verifies that the content hashes to
// the digest requested. A hand-rolled client did not, and a manifest that does
// not hash to what was asked for is exactly the input this action must not act
// on.
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
// Unlike the hand-rolled client this replaced, no token is exchanged here:
// go-containerregistry performs the exchange lazily on the first request, so a
// bad token surfaces at the first manifest read rather than at construction.
func NewClient(owner, packageName, githubToken string, warn func(string)) (*Client, error) {
	return newClient(Host, false, owner, packageName, githubToken, warn)
}

// newClient is NewClient with the registry located explicitly, so the tests can
// point it at an httptest server. plaintext is asked for rather than inferred
// from the host: a registry really served at localhost is still served over
// HTTPS unless the caller says otherwise.
func newClient(
	registryHost string, plaintext bool,
	owner, packageName, githubToken string, warn func(string),
) (*Client, error) {
	// Registry paths are lowercase, even when the GitHub owner or package name
	// is not.
	path := fmt.Sprintf("%s/%s/%s", registryHost,
		strings.ToLower(owner), strings.ToLower(packageName))

	options := []name.Option{}
	if plaintext {
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

// resetPuller replaces the puller, discarding its cached handshake, so the
// next attempt starts clean. NewPuller only fails on invalid options, which
// NewClient already validated; if it fails anyway the old puller stays, which
// at worst replays the error the caller is already seeing.
func (c *Client) resetPuller() {
	if puller, err := remote.NewPuller(c.options...); err == nil {
		c.puller = puller
	}
}

// labelWidth truncates a digest for log lines: "sha256:" plus twelve hex
// characters, the abbreviation docker itself prints.
const labelWidth = len("sha256:") + 12

// ReadManifest fetches a manifest by digest and returns the digests of the
// manifests under it, which is empty unless it is a multi-arch index.
//
// The puller negotiates the media types itself, so the explicit Accept list the
// hand-rolled client carried is no longer spelled out here; a single-platform
// manifest simply comes back with no children.
func (c *Client) ReadManifest(ctx context.Context, reference string) ([]string, error) {
	label := reference
	if len(label) > labelWidth {
		label = label[:labelWidth]
	}

	return retry.Do(ctx, func(ctx context.Context) ([]string, error) {
		descriptor, err := c.puller.Get(ctx, c.repository.Digest(reference))
		if err != nil {
			err = classify(err)
			// A fresh puller is only ever consumed by a retry, so keep the
			// cached handshake when no retry will happen. When one will, the
			// reset is deliberate collateral: a failed handshake and a failed
			// read are not reliably distinguishable across the library's error
			// types, so a transient read failure re-authenticates too, paying
			// two extra requests rather than risking a retry that replays a
			// cached handshake failure without reaching the network.
			var nonRetryable *retry.NonRetryableError
			if ctx.Err() == nil && !errors.As(err, &nonRetryable) {
				c.resetPuller()
			}
			return nil, err
		}

		if !descriptor.MediaType.IsIndex() {
			return nil, nil
		}

		index, err := descriptor.ImageIndex()
		if err != nil {
			return nil, classify(err)
		}
		indexManifest, err := index.IndexManifest()
		if err != nil {
			return nil, classify(err)
		}

		children := make([]string, 0, len(indexManifest.Manifests))
		for _, child := range indexManifest.Manifests {
			children = append(children, child.Digest.String())
		}
		return children, nil
	}, c.backoff.Options("manifest "+label))
}

// classify marks a registry error retryable or not by its HTTP status, so a 403
// or 404 fails immediately instead of burning the backoff.
func classify(err error) error {
	var transportErr *transport.Error
	if errors.As(err, &transportErr) {
		return retry.NewStatusError(err, transportErr.StatusCode)
	}
	return err
}
