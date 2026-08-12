/**
 * Unit tests for the packages API client, src/api.ts
 */
import { jest } from '@jest/globals'
import * as core from '../__fixtures__/core.js'

const get = jest.fn<(url: string, headers?: unknown) => Promise<unknown>>()
const del = jest.fn<(url: string, headers?: unknown) => Promise<unknown>>()

jest.unstable_mockModule('@actions/core', () => core)
// A plain class, not a jest.fn: `jest.resetAllMocks()` between tests would
// strip a mocked constructor's implementation, and clients are constructed
// after that reset.
jest.unstable_mockModule('@actions/http-client', () => ({
  HttpClient: class {
    get = get
    del = del
  }
}))

const { GitHubApi, nextPageUrl, tagsOf } = await import('../src/api.js')

/** Builds a minimal HttpClientResponse stand-in. */
function response(status: number, body: unknown, link?: string) {
  return {
    readBody: async () =>
      typeof body === 'string' ? body : JSON.stringify(body),
    message: { statusCode: status, headers: link ? { link } : {} }
  }
}

describe('nextPageUrl', () => {
  it('Returns undefined without a Link header or a next relation', () => {
    expect(nextPageUrl(undefined)).toBeUndefined()
    expect(nextPageUrl('<https://api/x?page=1>; rel="prev"')).toBeUndefined()
  })

  it('Extracts the next page URL', () => {
    const link =
      '<https://api/x?page=3>; rel="next", <https://api/x?page=9>; rel="last"'
    expect(nextPageUrl(link)).toBe('https://api/x?page=3')
  })
})

describe('tagsOf', () => {
  it('Returns an empty array when metadata is missing', () => {
    expect(tagsOf({ id: 1, name: 'sha256:a', updated_at: '' })).toEqual([])
  })
})

describe('GitHubApi', () => {
  afterEach(() => {
    jest.resetAllMocks()
  })

  it('Sends the token and API version headers', async () => {
    get.mockResolvedValue(response(200, { login: 'henrytill' }))

    await expect(new GitHubApi('tok').getAuthenticatedLogin()).resolves.toBe(
      'henrytill'
    )
    expect(get).toHaveBeenCalledWith(
      'https://api.github.com/user',
      expect.objectContaining({
        authorization: 'Bearer tok',
        'x-github-api-version': expect.any(String)
      })
    )
  })

  it('Follows pagination to the last page', async () => {
    const a = { id: 1, name: 'sha256:a', updated_at: '' }
    const b = { id: 2, name: 'sha256:b', updated_at: '' }
    get
      .mockResolvedValueOnce(
        response(200, [a], '<https://api.github.com/next>; rel="next"')
      )
      .mockResolvedValueOnce(response(200, [b]))

    const versions = await new GitHubApi('tok').listVersions(
      '/user/packages/container/p'
    )

    expect(versions).toEqual([a, b])
    expect(get).toHaveBeenNthCalledWith(
      1,
      'https://api.github.com/user/packages/container/p/versions?per_page=100',
      expect.anything()
    )
    expect(get).toHaveBeenNthCalledWith(
      2,
      'https://api.github.com/next',
      expect.anything()
    )
  })

  it('Throws on an error status, including the response body', async () => {
    get.mockResolvedValue(response(403, { message: 'Forbidden' }))

    await expect(new GitHubApi('tok').getAuthenticatedLogin()).rejects.toThrow(
      /403.*Forbidden/
    )
  })

  it('Deletes a version by id', async () => {
    del.mockResolvedValue(response(204, ''))

    await new GitHubApi('tok').deleteVersion('/user/packages/container/p', 7)

    expect(del).toHaveBeenCalledWith(
      'https://api.github.com/user/packages/container/p/versions/7',
      expect.anything()
    )
  })

  it('Honors GITHUB_API_URL for GitHub Enterprise', async () => {
    get.mockResolvedValue(response(200, { login: 'henrytill' }))

    await new GitHubApi(
      'tok',
      'https://ghe.example.com/api/v3/'
    ).getAuthenticatedLogin()

    expect(get).toHaveBeenCalledWith(
      'https://ghe.example.com/api/v3/user',
      expect.anything()
    )
  })
})
