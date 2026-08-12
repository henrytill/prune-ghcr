import { jest } from '@jest/globals'
import type { ContainerVersion, VersionsApi } from '../src/api.js'

export const getAuthenticatedLogin =
  jest.fn<VersionsApi['getAuthenticatedLogin']>()
export const listVersions = jest.fn<VersionsApi['listVersions']>()
export const deleteVersion = jest.fn<VersionsApi['deleteVersion']>()

export const api: VersionsApi = {
  getAuthenticatedLogin,
  listVersions,
  deleteVersion
}

/** Builds a version, tagged or not, with a controllable age. */
export function version(
  name: string,
  { id = 1, tags = [] as string[], ageHours = 24 } = {}
): ContainerVersion {
  return {
    id,
    name,
    updated_at: new Date(Date.now() - ageHours * 3600_000).toISOString(),
    metadata: { container: { tags } }
  }
}
