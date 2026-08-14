// Package registry reads image manifests from a container registry, which is
// how the action learns which untagged versions a tagged index still
// references.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/henrytill/prune-ghcr/internal/httpx"
	"github.com/henrytill/prune-ghcr/internal/retry"
)

// Host is the registry the action prunes.
const Host = "ghcr.io"

// DefaultBaseURL is where manifests are read from.
const DefaultBaseURL = "https://" + Host

// manifestAccept lists every manifest media type a tagged version can have.
// Without the index types the registry answers with a single platform manifest
// and its children stay invisible.
var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}, ", ")

// Manifest is an image manifest. Manifests is populated on a multi-arch index.
type Manifest struct {
	Manifests []Descriptor `json:"manifests"`
}

// Descriptor points at a manifest under an index.
type Descriptor struct {
	Digest string `json:"digest"`
}

// Client reads manifests from one repository.
type Client struct {
	http       *httpx.Client
	baseURL    string
	repository string
	token      string
	warn       func(string)
}

// NewClient exchanges a GitHub token for a pull-scoped registry token and
// returns a client authorized to read that repository's manifests.
//
// baseURL is injectable so the tests can point it at an httptest server; an
// empty baseURL selects ghcr.io.
func NewClient(ctx context.Context, baseURL, owner, packageName, githubToken string, warn func(string)) (*Client, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Registry paths are lowercase, even when the GitHub owner or package name
	// is not.
	repository := strings.ToLower(owner) + "/" + strings.ToLower(packageName)

	client := &Client{
		http:       httpx.NewClient(),
		baseURL:    baseURL,
		repository: repository,
		warn:       warn,
	}

	service := Host
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		service = parsed.Host
	}
	tokenURL := fmt.Sprintf("%s/token?service=%s&scope=%s",
		baseURL, url.QueryEscape(service),
		url.QueryEscape("repository:"+repository+":pull"))
	basic := base64.StdEncoding.EncodeToString([]byte(owner + ":" + githubToken))

	token, err := retry.Do(ctx, func(ctx context.Context) (string, error) {
		response, err := client.http.Do(ctx, http.MethodGet, tokenURL, map[string]string{
			"authorization": "Basic " + basic,
		})
		if err != nil {
			return "", err
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(response.Body, &body); err != nil {
			return "", &retry.NonRetryableError{
				Message: fmt.Sprintf("decoding registry token response: %s", err)}
		}
		if body.Token == "" {
			return "", &retry.NonRetryableError{Message: "registry token response had no token"}
		}
		return body.Token, nil
	}, retry.New("registry token request", warn))
	if err != nil {
		return nil, err
	}

	client.token = token
	return client, nil
}

// ReadManifest fetches a manifest by digest or tag.
func (c *Client) ReadManifest(ctx context.Context, reference string) (Manifest, error) {
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", c.baseURL, c.repository, reference)

	label := reference
	if len(label) > 19 {
		label = label[:19]
	}

	return retry.Do(ctx, func(ctx context.Context) (Manifest, error) {
		var manifest Manifest
		response, err := c.http.Do(ctx, http.MethodGet, manifestURL, map[string]string{
			"accept":        manifestAccept,
			"authorization": "Bearer " + c.token,
		})
		if err != nil {
			return manifest, err
		}
		if err := json.Unmarshal(response.Body, &manifest); err != nil {
			return manifest, &retry.NonRetryableError{
				Message: fmt.Sprintf("decoding manifest %s: %s", reference, err)}
		}
		return manifest, nil
	}, retry.New("manifest "+label, c.warn))
}
