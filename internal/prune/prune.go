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
	ListVersions(ctx context.Context, target api.Target) ([]api.ContainerVersion, error)
	DeleteVersion(ctx context.Context, target api.Target, id int64) error
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

// target resolves which packages API endpoints apply to a package.
//
// A user-owned package is reached through /user, which is the only path that
// can delete its versions; anything else is treated as an organization.
func target(owner, packageName, login string) api.Target {
	return api.Target{
		Owner:       owner,
		PackageName: packageName,
		UserOwned:   login == owner,
	}
}

// referenced returns the set of version names that must survive: every tagged
// version, plus every manifest a tagged index points at.
//
// The per-platform and attestation manifests under an image index carry no tags
// of their own, which is why a naive "delete every untagged version" deletes the
// contents of the image the tag points at.
//
// Only the tagged manifests themselves are read: a child that is itself an
// index would need its own children walked, and they are not. Nothing that
// pushes to GHCR produces nested indexes today -- buildx emits one level of
// platform and attestation manifests -- so the walk assumes one level rather
// than recursing speculatively.
func referenced(
	ctx context.Context,
	all []api.ContainerVersion,
	manifests ManifestReader,
	log Logger,
) (map[string]struct{}, error) {
	keep := make(map[string]struct{})
	tagged := 0
	for _, version := range all {
		if len(version.Tags) > 0 {
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
			return nil, fmt.Errorf("could not read %s: %w", digest, err)
		}
		for _, child := range manifest.Manifests {
			keep[child.Digest] = struct{}{}
		}
	}
	log.Info(fmt.Sprintf("    keeping %d version(s)", len(keep)))

	return keep, nil
}

// doomedVersions selects the untagged, unreferenced versions old enough to
// delete.
func doomedVersions(
	all []api.ContainerVersion,
	keep map[string]struct{},
	options Options,
	log Logger,
) []api.ContainerVersion {
	cutoff := time.Now().Add(-options.MinAge)

	var doomed []api.ContainerVersion
	for _, version := range all {
		if len(version.Tags) > 0 {
			continue
		}
		if _, kept := keep[version.Name]; kept {
			continue
		}

		if version.UpdatedAt.IsZero() {
			// An absent timestamp is unknown, not ancient. The TypeScript
			// compared NaN here, which is false, so a version whose updated_at
			// could not be read was deleted; skip it instead, on the same
			// refusal to guess as the unreadable manifest above.
			log.Error(fmt.Sprintf("skipping %s (no usable updated_at)", version.Name))
			continue
		}
		if version.UpdatedAt.After(cutoff) {
			log.Info(fmt.Sprintf("    skipping %s (younger than %s)", version.Name, options.MinAge))
			continue
		}

		doomed = append(doomed, version)
	}

	return doomed
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
	packageTarget := target(options.Owner, options.PackageName, login)

	log.Info(fmt.Sprintf("==> Listing versions of %s/%s/%s",
		registry.Host, options.Owner, options.PackageName))
	all, err := versions.ListVersions(ctx, packageTarget)
	if err != nil {
		return result, err
	}
	log.Info(fmt.Sprintf("    %d total", len(all)))

	keep, err := referenced(ctx, all, manifests, log)
	if err != nil {
		return result, err
	}

	doomed := doomedVersions(all, keep, options, log)

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
		if err := versions.DeleteVersion(ctx, packageTarget, version.ID); err != nil {
			result.Failed++
			log.Error(fmt.Sprintf("failed to delete %s: %s", version.Name, err))
			continue
		}
		result.Deleted++
	}
	log.Info(fmt.Sprintf("==> Deleted %d, failed %d", result.Deleted, result.Failed))

	return result, nil
}
