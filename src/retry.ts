import * as core from '@actions/core'

/** An error that retrying cannot fix, such as a 403 from the API. */
export class NonRetryableError extends Error {}

/**
 * @param status An HTTP status code.
 * @returns Whether a request that returned it is worth retrying.
 */
export function isRetryableStatus(status: number): boolean {
  return status === 408 || status === 429 || status >= 500
}

/**
 * Builds an error for a failed request, marking it retryable or not by status.
 *
 * @param message The error message.
 * @param status The HTTP status code the request returned.
 * @returns An `Error`, or a `NonRetryableError` for a permanent failure.
 */
export function statusError(message: string, status: number): Error {
  return isRetryableStatus(status)
    ? new Error(message)
    : new NonRetryableError(message)
}

export interface RetryOptions {
  /** Description of the operation, used in the retry log line. */
  what: string
  attempts?: number
  baseDelayMs?: number
}

/**
 * Runs `fn`, retrying on any failure with a linear backoff.
 *
 * Transient network and TLS failures against the API and the registry are
 * common enough to be worth retrying rather than failing the workflow.
 *
 * @param fn The operation to run.
 * @param options Retry behavior and the label used in log output.
 * @returns Resolves with the result of the first successful attempt.
 */
export async function withRetry<T>(
  fn: () => Promise<T>,
  { what, attempts = 3, baseDelayMs = 2000 }: RetryOptions
): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await fn()
    } catch (error) {
      if (attempt >= attempts || error instanceof NonRetryableError) throw error

      const message = error instanceof Error ? error.message : String(error)
      core.warning(
        `${what} failed (${message}); retry ${attempt}/${attempts - 1}`
      )
      await new Promise((resolve) => setTimeout(resolve, attempt * baseDelayMs))
    }
  }
}
