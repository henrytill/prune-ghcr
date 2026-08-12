import * as core from '@actions/core'
import { ContainerVersion, tagsOf, VersionsApi } from './api.js'
import { Manifest, ManifestReader, REGISTRY } from './registry.js'

export interface PruneOptions {
  owner: string
  packageName: string
  /** Skip versions younger than this many hours. */
  minAgeHours: number
  dryRun: boolean
}

export interface PruneResult {
  total: number
  kept: number
  deleted: number
  failed: number
}

/**
 * Resolves the packages API path for a package.
 *
 * A user-owned package lives under `/user`, which is the only path that can
 * delete its versions; anything else is treated as an organization.
 *
 * @param owner Owner of the package.
 * @param packageName Container package name.
 * @param login Login the token authenticates as.
 * @returns The API path the versions endpoints hang off.
 */
function packageBasePath(
  owner: string,
  packageName: string,
  login: string
): string {
  return login === owner
    ? `/user/packages/container/${packageName}`
    : `/orgs/${owner}/packages/container/${packageName}`
}

/**
 * Deletes untagged versions of a container package.
 *
 * Versions referenced by a tagged multi-arch index are preserved: the
 * per-platform and attestation manifests under an image index carry no tags of
 * their own, so a naive "delete every untagged version" deletes the contents of
 * the image the tag points at.
 *
 * @param options What to prune, and how.
 * @param api The packages API.
 * @param registry The registry, used to read tagged manifests.
 * @returns Counts of what was kept, deleted, and failed to delete.
 */
export async function prune(
  options: PruneOptions,
  api: VersionsApi,
  registry: ManifestReader
): Promise<PruneResult> {
  const { owner, packageName, minAgeHours, dryRun } = options
  const login = await api.getAuthenticatedLogin()
  const basePath = packageBasePath(owner, packageName, login)

  core.info(`==> Listing versions of ${REGISTRY}/${owner}/${packageName}`)
  const versions = await api.listVersions(basePath)
  core.info(`    ${versions.length} total`)

  const tagged = versions.filter((v) => tagsOf(v).length > 0)
  core.info(
    `==> Walking ${tagged.length} tagged manifest(s) for referenced children`
  )

  const keep = new Set(tagged.map((v) => v.name))
  for (const digest of [...keep].sort()) {
    let manifest: Manifest
    try {
      manifest = await registry.readManifest(digest)
    } catch (error) {
      // Deleting a child of a manifest we could not read would break a live
      // tag, so refuse to guess.
      const message = error instanceof Error ? error.message : String(error)
      throw new Error(`could not read ${digest}: ${message}`, { cause: error })
    }
    for (const child of manifest.manifests ?? []) keep.add(child.digest)
  }
  core.info(`    keeping ${keep.size} version(s)`)

  const cutoff = Date.now() - minAgeHours * 60 * 60 * 1000
  const doomed: ContainerVersion[] = []
  for (const version of versions) {
    if (tagsOf(version).length > 0 || keep.has(version.name)) continue
    if (Date.parse(version.updated_at) > cutoff) {
      core.info(`    skipping ${version.name} (younger than ${minAgeHours}h)`)
      continue
    }
    doomed.push(version)
  }

  const result: PruneResult = {
    total: versions.length,
    kept: versions.length - doomed.length,
    deleted: 0,
    failed: 0
  }

  if (doomed.length === 0) {
    core.info('==> Nothing to prune')
    return result
  }

  if (dryRun) {
    core.info(`==> Would delete ${doomed.length} untagged version(s):`)
    for (const version of doomed) core.info(`    ${version.name}`)
    return result
  }

  core.info(`==> Deleting ${doomed.length} untagged version(s)`)
  for (const version of doomed) {
    try {
      await api.deleteVersion(basePath, version.id)
      result.deleted++
    } catch (error) {
      result.failed++
      const message = error instanceof Error ? error.message : String(error)
      core.error(`failed to delete ${version.name}: ${message}`)
    }
  }
  core.info(`==> Deleted ${result.deleted}, failed ${result.failed}`)

  return result
}
