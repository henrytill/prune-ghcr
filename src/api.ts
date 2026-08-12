import { HttpClient } from '@actions/http-client'
import { statusError, withRetry } from './retry.js'

const API_VERSION = '2022-11-28'
const USER_AGENT = 'prune-ghcr'

/** A container package version, as returned by the packages API. */
export interface ContainerVersion {
  id: number
  name: string
  updated_at: string
  metadata?: { container?: { tags?: string[] } }
}

/** The subset of the packages API that {@link prune} depends on. */
export interface VersionsApi {
  getAuthenticatedLogin(): Promise<string>
  listVersions(basePath: string): Promise<ContainerVersion[]>
  deleteVersion(basePath: string, id: number): Promise<void>
}

/** Returns the tags of a version, or an empty array for an untagged one. */
export function tagsOf(version: ContainerVersion): string[] {
  return version.metadata?.container?.tags ?? []
}

/**
 * Extracts the `rel="next"` URL from a `Link` header, if there is one.
 *
 * @param link The raw `Link` header value.
 * @returns The next page URL, or undefined at the last page.
 */
export function nextPageUrl(link: string | undefined): string | undefined {
  if (!link) return undefined

  for (const part of link.split(',')) {
    const match = part.match(/<([^>]+)>\s*;\s*rel="next"/)
    if (match) return match[1]
  }

  return undefined
}

export class GitHubApi implements VersionsApi {
  private readonly client = new HttpClient(USER_AGENT)
  private readonly baseUrl: string

  constructor(
    private readonly token: string,
    baseUrl: string = process.env.GITHUB_API_URL ?? 'https://api.github.com'
  ) {
    this.baseUrl = baseUrl.replace(/\/+$/, '')
  }

  private get headers(): Record<string, string> {
    return {
      accept: 'application/vnd.github+json',
      authorization: `Bearer ${this.token}`,
      'x-github-api-version': API_VERSION
    }
  }

  private url(pathOrUrl: string): string {
    return pathOrUrl.startsWith('http')
      ? pathOrUrl
      : `${this.baseUrl}${pathOrUrl}`
  }

  private async getJson<T>(
    pathOrUrl: string
  ): Promise<{ body: T; link?: string }> {
    const url = this.url(pathOrUrl)
    return withRetry(
      async () => {
        const response = await this.client.get(url, this.headers)
        const text = await response.readBody()
        const status = response.message.statusCode ?? 0
        if (status < 200 || status >= 300) {
          throw statusError(
            `GET ${url} returned ${status}: ${text.trim()}`,
            status
          )
        }
        return {
          body: JSON.parse(text) as T,
          link: response.message.headers.link as string | undefined
        }
      },
      { what: `GET ${url}` }
    )
  }

  /** @returns The login of the user the token authenticates as. */
  async getAuthenticatedLogin(): Promise<string> {
    const { body } = await this.getJson<{ login: string }>('/user')
    return body.login
  }

  /**
   * Lists every version of a package, following pagination to the last page.
   *
   * @param basePath The package's API path, e.g. `/user/packages/container/foo`.
   * @returns All versions of the package.
   */
  async listVersions(basePath: string): Promise<ContainerVersion[]> {
    const versions: ContainerVersion[] = []
    let url: string | undefined = `${basePath}/versions?per_page=100`

    while (url) {
      const page = await this.getJson<ContainerVersion[]>(url)
      versions.push(...page.body)
      url = nextPageUrl(page.link)
    }

    return versions
  }

  async deleteVersion(basePath: string, id: number): Promise<void> {
    const url = this.url(`${basePath}/versions/${id}`)
    await withRetry(
      async () => {
        const response = await this.client.del(url, this.headers)
        const text = await response.readBody()
        const status = response.message.statusCode ?? 0
        if (status < 200 || status >= 300) {
          throw statusError(
            `DELETE ${url} returned ${status}: ${text.trim()}`,
            status
          )
        }
      },
      { what: `delete version ${id}` }
    )
  }
}
