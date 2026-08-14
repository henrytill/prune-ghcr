// Package api is a client for the parts of the GitHub packages REST API the
// action uses: identifying the token's owner, listing a package's versions, and
// deleting one.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/henrytill/prune-ghcr/internal/httpx"
	"github.com/henrytill/prune-ghcr/internal/retry"
)

// APIVersion pins the REST API's documented behavior.
const APIVersion = "2022-11-28"

// DefaultBaseURL is the public API. GITHUB_API_URL overrides it on Enterprise.
const DefaultBaseURL = "https://api.github.com"

// ContainerVersion is a container package version, as returned by the packages
// API.
type ContainerVersion struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	UpdatedAt string   `json:"updated_at"`
	Metadata  Metadata `json:"metadata"`
}

// Metadata is the package-type-specific part of a version.
type Metadata struct {
	Container ContainerMetadata `json:"container"`
}

// ContainerMetadata carries the tags of a container version.
type ContainerMetadata struct {
	Tags []string `json:"tags"`
}

// Tags returns the tags of a version, or nothing for an untagged one.
func (v ContainerVersion) Tags() []string { return v.Metadata.Container.Tags }

// nextPattern matches the next-page entry of a Link header.
var nextPattern = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// NextPageURL extracts the rel="next" URL from a Link header, and returns the
// empty string at the last page.
func NextPageURL(link string) string {
	if link == "" {
		return ""
	}
	for part := range strings.SplitSeq(link, ",") {
		if match := nextPattern.FindStringSubmatch(part); match != nil {
			return match[1]
		}
	}
	return ""
}

// Client is a packages API client.
type Client struct {
	http    *httpx.Client
	token   string
	baseURL string
	warn    func(string)
}

// NewClient returns a client authenticating with token against baseURL.
//
// An empty baseURL selects the public API. warn receives one line per retry.
func NewClient(token, baseURL string, warn func(string)) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		http:    httpx.NewClient(),
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		warn:    warn,
	}
}

func (c *Client) headers() map[string]string {
	return map[string]string{
		"accept":               "application/vnd.github+json",
		"authorization":        "Bearer " + c.token,
		"x-github-api-version": APIVersion,
	}
}

// url resolves a path against the base URL, passing absolute URLs through so
// that pagination can follow the Link header verbatim.
func (c *Client) url(pathOrURL string) string {
	if strings.HasPrefix(pathOrURL, "http") {
		return pathOrURL
	}
	return c.baseURL + pathOrURL
}

// get performs a GET, retrying transient failures, and decodes the body into v.
// It returns the response's Link header.
func (c *Client) get(ctx context.Context, pathOrURL string, v any) (string, error) {
	url := c.url(pathOrURL)
	response, err := retry.Do(ctx, func(ctx context.Context) (*httpx.Response, error) {
		return c.http.Do(ctx, http.MethodGet, url, c.headers())
	}, retry.New("GET "+url, c.warn))
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal(response.Body, v); err != nil {
		return "", &retry.NonRetryableError{Message: fmt.Sprintf("decoding %s: %s", url, err)}
	}
	return response.Header.Get("link"), nil
}

// AuthenticatedLogin returns the login of the user the token authenticates as.
func (c *Client) AuthenticatedLogin(ctx context.Context) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if _, err := c.get(ctx, "/user", &user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// ListVersions lists every version of a package, following pagination to the
// last page.
//
// basePath is the package's API path, e.g. /user/packages/container/foo.
func (c *Client) ListVersions(ctx context.Context, basePath string) ([]ContainerVersion, error) {
	var versions []ContainerVersion

	for url := basePath + "/versions?per_page=100"; url != ""; {
		var page []ContainerVersion
		link, err := c.get(ctx, url, &page)
		if err != nil {
			return nil, err
		}
		versions = append(versions, page...)
		url = NextPageURL(link)
	}

	return versions, nil
}

// DeleteVersion deletes one version of a package by id.
func (c *Client) DeleteVersion(ctx context.Context, basePath string, id int64) error {
	url := c.url(fmt.Sprintf("%s/versions/%d", basePath, id))
	_, err := retry.Do(ctx, func(ctx context.Context) (*httpx.Response, error) {
		return c.http.Do(ctx, http.MethodDelete, url, c.headers())
	}, retry.New(fmt.Sprintf("delete version %d", id), c.warn))
	return err
}
