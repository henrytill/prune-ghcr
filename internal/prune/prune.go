// Package prune deletes untagged versions of a container package while keeping
// any that a tagged multi-arch index still references.
package prune

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/henrytill/prune-ghcr/internal/api"
	"github.com/henrytill/prune-ghcr/internal/registry"
)

// VersionsAPI is the subset of the packages API this package depends on.
type VersionsAPI interface {
	AuthenticatedLogin(ctx context.Context) (string, error)
	ListVersions(ctx context.Context, basePath string) ([]api.ContainerVersion, error)
	DeleteVersion(ctx context.Context, basePath string, id int64) error
}

// ManifestReader is the subset of the registry this package depends on.
type ManifestReader interface {
	ReadManifest(ctx context.Context, reference string) (registry.Manifest, error)
}

// Logger receives the action's log output.
type Logger interface {
	Info(string)
	Error(string)
}

// Options is what to prune, and how.
type Options struct {
	Owner       string
	PackageName string
	// MinAge skips versions younger than this.
	MinAge time.Duration
	DryRun bool
}

// Result counts what was kept, deleted, and failed to delete.
type Result struct {
	Total   int
	Kept    int
	Deleted int
	Failed  int
}

// basePath resolves the packages API path for a package.
//
// A user-owned package lives under /user, which is the only path that can
// delete its versions; anything else is treated as an organization.
func basePath(owner, packageName, login string) string {
	if login == owner {
		return "/user/packages/container/" + packageName
	}
	return "/orgs/" + owner + "/packages/container/" + packageName
}

// Prune deletes untagged versions of a container package.
//
// Versions referenced by a tagged multi-arch index are preserved: the
// per-platform and attestation manifests under an image index carry no tags of
// their own, so a naive "delete every untagged version" deletes the contents of
// the image the tag points at.
func Prune(
	ctx context.Context,
	options Options,
	versions VersionsAPI,
	manifests ManifestReader,
	log Logger,
) (Result, error) {
	var result Result

	login, err := versions.AuthenticatedLogin(ctx)
	if err != nil {
		return result, err
	}
	path := basePath(options.Owner, options.PackageName, login)

	log.Info(fmt.Sprintf("==> Listing versions of %s/%s/%s",
		registry.Host, options.Owner, options.PackageName))
	all, err := versions.ListVersions(ctx, path)
	if err != nil {
		return result, err
	}
	log.Info(fmt.Sprintf("    %d total", len(all)))

	keep := make(map[string]struct{})
	tagged := 0
	for _, version := range all {
		if len(version.Tags()) > 0 {
			tagged++
			keep[version.Name] = struct{}{}
		}
	}
	log.Info(fmt.Sprintf("==> Walking %d tagged manifest(s) for referenced children", tagged))

	// Sorted so the log output is reproducible: map iteration order is
	// randomized.
	for _, digest := range slices.Sorted(maps.Keys(keep)) {
		manifest, err := manifests.ReadManifest(ctx, digest)
		if err != nil {
			// Deleting a child of a manifest we could not read would break a
			// live tag, so refuse to guess.
			return result, fmt.Errorf("could not read %s: %w", digest, err)
		}
		for _, child := range manifest.Manifests {
			keep[child.Digest] = struct{}{}
		}
	}
	log.Info(fmt.Sprintf("    keeping %d version(s)", len(keep)))

	cutoff := time.Now().Add(-options.MinAge)
	var doomed []api.ContainerVersion
	for _, version := range all {
		if len(version.Tags()) > 0 {
			continue
		}
		if _, kept := keep[version.Name]; kept {
			continue
		}

		updated, err := time.Parse(time.RFC3339, version.UpdatedAt)
		if err != nil {
			// The TypeScript version compared NaN here, which is false, so an
			// unparseable timestamp meant the version got deleted. Skip it
			// instead: this is the same refusal to guess as above.
			log.Error(fmt.Sprintf("skipping %s (unparseable updated_at %q: %s)",
				version.Name, version.UpdatedAt, err))
			continue
		}
		if updated.After(cutoff) {
			log.Info(fmt.Sprintf("    skipping %s (younger than %s)", version.Name, options.MinAge))
			continue
		}

		doomed = append(doomed, version)
	}

	result.Total = len(all)
	result.Kept = len(all) - len(doomed)

	if len(doomed) == 0 {
		log.Info("==> Nothing to prune")
		return result, nil
	}

	if options.DryRun {
		log.Info(fmt.Sprintf("==> Would delete %d untagged version(s):", len(doomed)))
		for _, version := range doomed {
			log.Info("    " + version.Name)
		}
		return result, nil
	}

	log.Info(fmt.Sprintf("==> Deleting %d untagged version(s)", len(doomed)))
	for _, version := range doomed {
		if err := versions.DeleteVersion(ctx, path, version.ID); err != nil {
			result.Failed++
			log.Error(fmt.Sprintf("failed to delete %s: %s", version.Name, err))
			continue
		}
		result.Deleted++
	}
	log.Info(fmt.Sprintf("==> Deleted %d, failed %d", result.Deleted, result.Failed))

	return result, nil
}
