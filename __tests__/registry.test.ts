/**
 * Unit tests for the registry client, src/registry.ts
 */
import { jest } from '@jest/globals'
import * as core from '../__fixtures__/core.js'

const get = jest.fn<(url: string, headers?: unknown) => Promise<unknown>>()

jest.unstable_mockModule('@actions/core', () => core)
// See api.test.ts: a mocked constructor would not survive resetAllMocks.
jest.unstable_mockModule('@actions/http-client', () => ({
  HttpClient: class {
    get = get
  }
}))

const { RegistryClient } = await import('../src/registry.js')

function response(status: number, body: unknown) {
  return {
    readBody: async () => JSON.stringify(body),
    message: { statusCode: status, headers: {} }
  }
}

describe('registry.ts', () => {
  afterEach(() => {
    jest.resetAllMocks()
  })

  it('Exchanges the GitHub token for a pull-scoped registry token', async () => {
    get.mockResolvedValue(response(200, { token: 'registry-token' }))

    await RegistryClient.create('HenryTill', 'Devcontainer-Debian', 'ghp_tok')

    const [url, headers] = get.mock.calls[0] as [string, Record<string, string>]
    // Registry repository paths are lowercase even when the package name is not.
    expect(url).toContain('scope=repository:henrytill/devcontainer-debian:pull')
    expect(headers.authorization).toBe(
      `Basic ${Buffer.from('HenryTill:ghp_tok').toString('base64')}`
    )
  })

  it('Throws when the token response carries no token', async () => {
    get.mockResolvedValue(response(200, {}))

    await expect(
      RegistryClient.create('henrytill', 'p', 'ghp_tok')
    ).rejects.toThrow(/no token/)
  })

  it('Requests manifests with every index media type accepted', async () => {
    get.mockResolvedValueOnce(response(200, { token: 'registry-token' }))
    const client = await RegistryClient.create('henrytill', 'p', 'ghp_tok')

    get.mockResolvedValueOnce(
      response(200, { manifests: [{ digest: 'sha256:child' }] })
    )
    const manifest = await client.readManifest('sha256:index')

    expect(manifest).toEqual({ manifests: [{ digest: 'sha256:child' }] })
    const [url, headers] = get.mock.calls[1] as [string, Record<string, string>]
    expect(url).toBe('https://ghcr.io/v2/henrytill/p/manifests/sha256:index')
    expect(headers.authorization).toBe('Bearer registry-token')
    expect(headers.accept).toContain('application/vnd.oci.image.index.v1+json')
    expect(headers.accept).toContain(
      'application/vnd.docker.distribution.manifest.list.v2+json'
    )
  })

  it('Throws on an error status', async () => {
    get.mockResolvedValueOnce(response(200, { token: 'registry-token' }))
    const client = await RegistryClient.create('henrytill', 'p', 'ghp_tok')

    get.mockResolvedValue(response(404, { errors: [] }))

    await expect(client.readManifest('sha256:missing')).rejects.toThrow(/404/)
  })
})
