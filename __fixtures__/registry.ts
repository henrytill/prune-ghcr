import { jest } from '@jest/globals'
import type { ManifestReader } from '../src/registry.js'

export const readManifest = jest.fn<ManifestReader['readManifest']>()

export const registry: ManifestReader = { readManifest }
