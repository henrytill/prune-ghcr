/**
 * Unit tests for the pruning logic, src/prune.ts
 */
import { jest } from '@jest/globals'
import {
  api,
  deleteVersion,
  getAuthenticatedLogin,
  listVersions,
  version
} from '../__fixtures__/api.js'
import * as core from '../__fixtures__/core.js'
import { readManifest, registry } from '../__fixtures__/registry.js'

jest.unstable_mockModule('@actions/core', () => core)

const { prune } = await import('../src/prune.js')

const options = {
  owner: 'henrytill',
  packageName: 'devcontainer-debian',
  minAgeHours: 0,
  dryRun: false
}

describe('prune.ts', () => {
  beforeEach(() => {
    getAuthenticatedLogin.mockResolvedValue('henrytill')
    readManifest.mockResolvedValue({})
    deleteVersion.mockResolvedValue()
  })

  afterEach(() => {
    jest.resetAllMocks()
  })

  it('Uses the /user path when the token owns the package', async () => {
    listVersions.mockResolvedValue([version('sha256:aa', { id: 1 })])

    await prune(options, api, registry)

    expect(listVersions).toHaveBeenCalledWith(
      '/user/packages/container/devcontainer-debian'
    )
    expect(deleteVersion).toHaveBeenCalledWith(
      '/user/packages/container/devcontainer-debian',
      1
    )
  })

  it('Uses the /orgs path for a package owned by someone else', async () => {
    getAuthenticatedLogin.mockResolvedValue('someone-else')
    listVersions.mockResolvedValue([])

    await prune(options, api, registry)

    expect(listVersions).toHaveBeenCalledWith(
      '/orgs/henrytill/packages/container/devcontainer-debian'
    )
  })

  it('Keeps tagged versions and the children of a tagged index', async () => {
    listVersions.mockResolvedValue([
      version('sha256:index', { id: 1, tags: ['latest'] }),
      version('sha256:amd64', { id: 2 }),
      version('sha256:arm64', { id: 3 }),
      version('sha256:orphan', { id: 4 })
    ])
    readManifest.mockResolvedValue({
      manifests: [{ digest: 'sha256:amd64' }, { digest: 'sha256:arm64' }]
    })

    const result = await prune(options, api, registry)

    expect(readManifest).toHaveBeenCalledWith('sha256:index')
    expect(deleteVersion).toHaveBeenCalledTimes(1)
    expect(deleteVersion).toHaveBeenCalledWith(expect.any(String), 4)
    expect(result).toEqual({ total: 4, kept: 3, deleted: 1, failed: 0 })
  })

  it('Refuses to prune when a tagged manifest cannot be read', async () => {
    listVersions.mockResolvedValue([
      version('sha256:index', { id: 1, tags: ['latest'] }),
      version('sha256:orphan', { id: 2 })
    ])
    readManifest.mockRejectedValue(new Error('502 Bad Gateway'))

    await expect(prune(options, api, registry)).rejects.toThrow(
      /could not read sha256:index/
    )
    expect(deleteVersion).not.toHaveBeenCalled()
  })

  it('Skips versions younger than min-age-hours', async () => {
    listVersions.mockResolvedValue([
      version('sha256:fresh', { id: 1, ageHours: 0.5 }),
      version('sha256:stale', { id: 2, ageHours: 5 })
    ])

    const result = await prune({ ...options, minAgeHours: 1 }, api, registry)

    expect(deleteVersion).toHaveBeenCalledTimes(1)
    expect(deleteVersion).toHaveBeenCalledWith(expect.any(String), 2)
    expect(result).toEqual({ total: 2, kept: 1, deleted: 1, failed: 0 })
  })

  it('Deletes nothing in dry-run mode', async () => {
    listVersions.mockResolvedValue([version('sha256:orphan', { id: 1 })])

    const result = await prune({ ...options, dryRun: true }, api, registry)

    expect(deleteVersion).not.toHaveBeenCalled()
    expect(result).toEqual({ total: 1, kept: 0, deleted: 0, failed: 0 })
  })

  it('Reports nothing to prune when every version is referenced', async () => {
    listVersions.mockResolvedValue([
      version('sha256:index', { id: 1, tags: ['latest'] })
    ])

    const result = await prune(options, api, registry)

    expect(deleteVersion).not.toHaveBeenCalled()
    expect(result).toEqual({ total: 1, kept: 1, deleted: 0, failed: 0 })
  })

  it('Counts delete failures and continues', async () => {
    listVersions.mockResolvedValue([
      version('sha256:a', { id: 1 }),
      version('sha256:b', { id: 2 })
    ])
    deleteVersion.mockRejectedValueOnce(new Error('403 Forbidden'))

    const result = await prune(options, api, registry)

    expect(deleteVersion).toHaveBeenCalledTimes(2)
    expect(core.error).toHaveBeenCalledWith(
      expect.stringContaining('failed to delete sha256:a')
    )
    expect(result).toEqual({ total: 2, kept: 0, deleted: 1, failed: 1 })
  })
})
