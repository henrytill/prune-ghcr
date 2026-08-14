// Package api is a client for the parts of the GitHub packages REST API the
// action uses: identifying the token's owner, listing a package's versions, and
// deleting one.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/go-github/v90/github"

	"github.com/henrytill/prune-ghcr/internal/retry"
)

// packageType is the only package type this action prunes.
const packageType = "container"

// pageSize is the maximum the versions endpoint allows.
const pageSize = 100

// Timeout bounds a single request.
const Timeout = 30 * time.Second

// Target identifies the package to operate on.
//
// UserOwned selects the /user/packages endpoints, which are the only ones that
// can delete versions of a package owned by a user rather than an organization.
type Target struct {
	Owner       string
	PackageName string
	UserOwned   bool
}

// ContainerVersion is a container package version.
//
// It is this package's own type rather than github.PackageVersion so that the
// pointer dereferencing stays here, and so prune does not depend on go-github.
type ContainerVersion struct {
	ID   int64
	Name string
	// UpdatedAt is the zero time when the API did not report one, which callers
	// must treat as unknown rather than as very old.
	UpdatedAt time.Time
	Tags      []string
}

// Client is a packages API client.
type Client struct {
	github *github.Client
	warn   func(string)
}

// NewClient returns a client authenticating with token against baseURL.
//
// An empty baseURL selects the public API. warn receives one line per retry.
func NewClient(token, baseURL string, warn func(string)) (*Client, error) {
	// The timeout is explicit: go-github builds on net/http, which has none by
	// default, and a wedged read would otherwise hang the job rather than fail
	// and retry.
	options := []github.ClientOptionsFunc{
		github.WithAuthToken(token),
		github.WithTimeout(Timeout),
	}
	if baseURL != "" {
		options = append(options, github.WithEnterpriseURLs(baseURL, baseURL))
	}

	client, err := github.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("configuring the API client: %w", err)
	}

	return &Client{github: client, warn: warn}, nil
}

// statusError converts a go-github failure into an error that says whether
// retrying could help, so a 403 or 404 fails immediately instead of burning
// the backoff.
func statusError(response *github.Response, err error) error {
	if response == nil || response.Response == nil {
		return err
	}
	return retry.StatusError(err.Error(), response.StatusCode)
}

// AuthenticatedLogin returns the login of the user the token authenticates as.
func (c *Client) AuthenticatedLogin(ctx context.Context) (string, error) {
	return retry.Do(ctx, func(ctx context.Context) (string, error) {
		user, response, err := c.github.Users.Get(ctx, "")
		if err != nil {
			return "", statusError(response, err)
		}
		return user.GetLogin(), nil
	}, retry.New("GET /user", c.warn))
}

// versionsPage carries a page of results together with the cursor to the next,
// because a retried call can only yield one value.
type versionsPage struct {
	versions []*github.PackageVersion
	nextPage int
}

// ListVersions lists every version of a package, following pagination to the
// last page.
func (c *Client) ListVersions(ctx context.Context, target Target) ([]ContainerVersion, error) {
	var versions []ContainerVersion
	options := github.ListOptions{PerPage: pageSize}

	for {
		page, err := retry.Do(ctx, func(ctx context.Context) (versionsPage, error) {
			found, response, err := c.listPage(ctx, target, options)
			if err != nil {
				return versionsPage{}, statusError(response, err)
			}
			next := 0
			if response != nil {
				next = response.NextPage
			}
			return versionsPage{versions: found, nextPage: next}, nil
		}, retry.New("list versions", c.warn))
		if err != nil {
			return nil, err
		}

		for _, version := range page.versions {
			converted, err := convert(version)
			if err != nil {
				return nil, err
			}
			versions = append(versions, converted)
		}

		if page.nextPage == 0 {
			return versions, nil
		}
		options.Page = page.nextPage
	}
}

// listPage dispatches to the user or organization endpoint.
//
// The /user path is the only one that can list and then delete versions of a
// package owned by a user, so it is selected by ownership rather than by name.
func (c *Client) listPage(
	ctx context.Context, target Target, options github.ListOptions,
) ([]*github.PackageVersion, *github.Response, error) {
	if target.UserOwned {
		return c.github.Users.ListPackageVersions(ctx, packageType, target.PackageName,
			&github.ListPackageVersionsOptions{ListOptions: options})
	}
	return c.github.Organizations.PackageGetAllVersions(ctx, target.Owner, packageType,
		target.PackageName, &github.PackageListOptions{ListOptions: options})
}

// DeleteVersion deletes one version of a package by id.
func (c *Client) DeleteVersion(ctx context.Context, target Target, id int64) error {
	_, err := retry.Do(ctx, func(ctx context.Context) (struct{}, error) {
		var response *github.Response
		var err error
		if target.UserOwned {
			// The empty user means the authenticated user, which is the
			// /user/packages path.
			response, err = c.github.Users.PackageDeleteVersion(ctx, "", packageType,
				target.PackageName, id)
		} else {
			response, err = c.github.Organizations.PackageDeleteVersion(ctx, target.Owner,
				packageType, target.PackageName, id)
		}
		if err != nil {
			return struct{}{}, statusError(response, err)
		}
		return struct{}{}, nil
	}, retry.New(fmt.Sprintf("delete version %d", id), c.warn))
	return err
}

// convert flattens a go-github package version.
//
// Metadata is json.RawMessage rather than a struct, because the same field is
// an array on webhook payloads, so the container tags have to be decoded here.
func convert(version *github.PackageVersion) (ContainerVersion, error) {
	converted := ContainerVersion{
		ID:   version.GetID(),
		Name: version.GetName(),
	}
	if version.UpdatedAt != nil {
		converted.UpdatedAt = version.UpdatedAt.Time
	}

	if len(version.Metadata) > 0 {
		var metadata github.PackageMetadata
		if err := json.Unmarshal(version.Metadata, &metadata); err != nil {
			return converted, &retry.NonRetryableError{
				Message: fmt.Sprintf("decoding metadata of %s: %s", converted.Name, err)}
		}
		if metadata.Container != nil {
			converted.Tags = metadata.Container.Tags
		}
	}

	return converted, nil
}
