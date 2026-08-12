/**
 * Unit tests for the action's main functionality, src/main.ts
 *
 * To mock dependencies in ESM, you can create fixtures that export mock
 * functions and objects. For example, the core module is mocked in this test,
 * so that the actual '@actions/core' module is not imported.
 */
import { jest } from '@jest/globals'
import * as core from '../__fixtures__/core.js'
import { api } from '../__fixtures__/api.js'
import { registry } from '../__fixtures__/registry.js'
import type { prune as pruneType } from '../src/prune.js'

const GitHubApi = jest.fn(() => api)
const create = jest.fn(async () => registry)
const prune = jest.fn<typeof pruneType>()

jest.unstable_mockModule('@actions/core', () => core)
jest.unstable_mockModule('../src/api.js', () => ({ GitHubApi }))
jest.unstable_mockModule('../src/registry.js', () => ({
  RegistryClient: { create }
}))
jest.unstable_mockModule('../src/prune.js', () => ({ prune }))

const { run } = await import('../src/main.js')

const inputs: Record<string, string> = {
  token: 'ghp_token',
  owner: 'henrytill',
  package: 'devcontainer-debian',
  'min-age-hours': '0'
}

describe('main.ts', () => {
  beforeEach(() => {
    core.getInput.mockImplementation((name: string) => inputs[name] ?? '')
    core.getBooleanInput.mockImplementation(() => false)
    prune.mockResolvedValue({ total: 3, kept: 2, deleted: 1, failed: 0 })
  })

  afterEach(() => {
    jest.resetAllMocks()
  })

  it('Prunes with the inputs and sets outputs', async () => {
    await run()

    expect(create).toHaveBeenCalledWith(
      'henrytill',
      'devcontainer-debian',
      'ghp_token'
    )
    expect(prune).toHaveBeenCalledWith(
      {
        owner: 'henrytill',
        packageName: 'devcontainer-debian',
        minAgeHours: 0,
        dryRun: false
      },
      api,
      registry
    )
    expect(core.setOutput).toHaveBeenCalledWith('deleted', 1)
    expect(core.setOutput).toHaveBeenCalledWith('kept', 2)
    expect(core.setOutput).toHaveBeenCalledWith('failed', 0)
    expect(core.setFailed).not.toHaveBeenCalled()
  })

  it('Strips whitespace from the token', async () => {
    core.getInput.mockImplementation((name: string) =>
      name === 'token' ? 'ghp_token\n' : (inputs[name] ?? '')
    )

    await run()

    expect(create).toHaveBeenCalledWith(
      'henrytill',
      'devcontainer-debian',
      'ghp_token'
    )
  })

  it('Fails when the token is empty', async () => {
    core.getInput.mockImplementation((name: string) =>
      name === 'token' ? '  \n' : (inputs[name] ?? '')
    )

    await run()

    expect(core.setFailed).toHaveBeenCalledWith(
      expect.stringContaining('token input is empty')
    )
    expect(prune).not.toHaveBeenCalled()
  })

  it('Fails on a min-age-hours that is not a non-negative number', async () => {
    core.getInput.mockImplementation((name: string) =>
      name === 'min-age-hours' ? 'soon' : (inputs[name] ?? '')
    )

    await run()

    expect(core.setFailed).toHaveBeenCalledWith(
      expect.stringContaining('min-age-hours')
    )
    expect(prune).not.toHaveBeenCalled()
  })

  it('Fails the run when a delete failed', async () => {
    prune.mockResolvedValue({ total: 3, kept: 1, deleted: 1, failed: 1 })

    await run()

    expect(core.setFailed).toHaveBeenCalledWith('failed to delete 1 version(s)')
  })

  it('Fails the run on an unexpected error', async () => {
    prune.mockRejectedValue(new Error('boom'))

    await run()

    expect(core.setFailed).toHaveBeenCalledWith('boom')
  })
})
