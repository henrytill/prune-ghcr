import { HttpClient } from '@actions/http-client'
import { NonRetryableError, statusError, withRetry } from './retry.js'

export const REGISTRY = 'ghcr.io'

const USER_AGENT = 'prune-ghcr'

/**
 * Every manifest media type a tagged version can have. Without the index types
 * the registry answers with a single platform manifest and its children stay
 * invisible.
 */
const MANIFEST_ACCEPT = [
  'application/vnd.oci.image.index.v1+json',
  'application/vnd.docker.distribution.manifest.list.v2+json',
  'application/vnd.docker.distribution.manifest.v2+json',
  'application/vnd.oci.image.manifest.v1+json'
].join(', ')

/** An image manifest; `manifests` is present on a multi-arch index. */
export interface Manifest {
  manifests?: { digest: string }[]
}

/** The subset of the registry that {@link prune} depends on. */
export interface ManifestReader {
  readManifest(reference: string): Promise<Manifest>
}

export class RegistryClient implements ManifestReader {
  private readonly client = new HttpClient(USER_AGENT)

  private constructor(
    private readonly repository: string,
    private readonly token: string
  ) {}

  /**
   * Exchanges a GitHub token for a registry pull token.
   *
   * @param owner Owner of the package.
   * @param packageName Container package name.
   * @param githubToken Token with read access to the package.
   * @returns A client authorized to read that repository's manifests.
   */
  static async create(
    owner: string,
    packageName: string,
    githubToken: string
  ): Promise<RegistryClient> {
    // Registry paths are lowercase, even when the GitHub owner or package name
    // is not.
    const repository = `${owner.toLowerCase()}/${packageName.toLowerCase()}`
    const url =
      `https://${REGISTRY}/token?service=${REGISTRY}` +
      `&scope=repository:${repository}:pull`
    const basic = Buffer.from(`${owner}:${githubToken}`).toString('base64')

    const token = await withRetry(
      async () => {
        const response = await new HttpClient(USER_AGENT).get(url, {
          authorization: `Basic ${basic}`
        })
        const text = await response.readBody()
        const status = response.message.statusCode ?? 0
        if (status < 200 || status >= 300) {
          throw statusError(`registry token request returned ${status}`, status)
        }
        const body = JSON.parse(text) as { token?: string }
        if (!body.token) {
          throw new NonRetryableError('registry token response had no token')
        }
        return body.token
      },
      { what: 'registry token request' }
    )

    return new RegistryClient(repository, token)
  }

  /**
   * Fetches a manifest by digest or tag.
   *
   * @param reference A digest or tag.
   * @returns The parsed manifest.
   */
  async readManifest(reference: string): Promise<Manifest> {
    const url = `https://${REGISTRY}/v2/${this.repository}/manifests/${reference}`
    return withRetry(
      async () => {
        const response = await this.client.get(url, {
          accept: MANIFEST_ACCEPT,
          authorization: `Bearer ${this.token}`
        })
        const text = await response.readBody()
        const status = response.message.statusCode ?? 0
        if (status < 200 || status >= 300) {
          throw statusError(
            `manifest request returned ${status}: ${text.trim()}`,
            status
          )
        }
        return JSON.parse(text) as Manifest
      },
      { what: `manifest ${reference.slice(0, 19)}` }
    )
  }
}
