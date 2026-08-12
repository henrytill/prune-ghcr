/**
 * Unit tests for the retry helper, src/retry.ts
 */
import { jest } from '@jest/globals'
import * as core from '../__fixtures__/core.js'

jest.unstable_mockModule('@actions/core', () => core)

const { isRetryableStatus, NonRetryableError, statusError, withRetry } =
  await import('../src/retry.js')

const options = { what: 'thing', baseDelayMs: 0 }

describe('retry.ts', () => {
  afterEach(() => {
    jest.resetAllMocks()
  })

  it('Returns the result without retrying on success', async () => {
    const fn = jest.fn(async () => 'ok')

    await expect(withRetry(fn, options)).resolves.toBe('ok')
    expect(fn).toHaveBeenCalledTimes(1)
    expect(core.warning).not.toHaveBeenCalled()
  })

  it('Retries a transient failure and warns', async () => {
    const fn = jest
      .fn<() => Promise<string>>()
      .mockRejectedValueOnce(new Error('ECONNRESET'))
      .mockResolvedValueOnce('ok')

    await expect(withRetry(fn, options)).resolves.toBe('ok')
    expect(fn).toHaveBeenCalledTimes(2)
    expect(core.warning).toHaveBeenCalledWith(
      expect.stringContaining('thing failed (ECONNRESET)')
    )
  })

  it('Does not retry an error retrying cannot fix', async () => {
    const fn = jest
      .fn<() => Promise<string>>()
      .mockRejectedValue(new NonRetryableError('403 Forbidden'))

    await expect(withRetry(fn, options)).rejects.toThrow('403 Forbidden')
    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('Treats transient statuses as retryable and the rest as permanent', () => {
    expect(isRetryableStatus(500)).toBe(true)
    expect(isRetryableStatus(429)).toBe(true)
    expect(isRetryableStatus(408)).toBe(true)
    expect(isRetryableStatus(403)).toBe(false)
    expect(isRetryableStatus(404)).toBe(false)

    expect(statusError('m', 502)).not.toBeInstanceOf(NonRetryableError)
    expect(statusError('m', 404)).toBeInstanceOf(NonRetryableError)
  })

  it('Rethrows after the last attempt', async () => {
    const fn = jest
      .fn<() => Promise<string>>()
      .mockRejectedValue(new Error('down'))

    await expect(withRetry(fn, { ...options, attempts: 2 })).rejects.toThrow(
      'down'
    )
    expect(fn).toHaveBeenCalledTimes(2)
  })
})
