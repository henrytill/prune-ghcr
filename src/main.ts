import * as core from '@actions/core'
import { GitHubApi } from './api.js'
import { prune } from './prune.js'
import { RegistryClient } from './registry.js'

/**
 * The main function for the action.
 *
 * @returns Resolves when the action is complete.
 */
export async function run(): Promise<void> {
  try {
    // A PAT pasted into a secret with a trailing newline makes for an invalid
    // Authorization header, and PATs contain no whitespace of their own. An
    // empty token is then a misconfiguration rather than an opt-out -- every
    // consuming repo is expected to hold a PAT -- so fail here instead of
    // leaving a green run that stopped pruning.
    const token = core.getInput('token').replace(/\s/g, '')
    if (!token) {
      core.setFailed('token input is empty (is the PAT secret set?)')
      return
    }

    // Masks the trimmed form too: the raw secret is already masked, but the
    // string actually sent differs from it if the secret had whitespace.
    core.setSecret(token)

    const owner = core.getInput('owner', { required: true })
    const packageName = core.getInput('package', { required: true })
    const dryRun = core.getBooleanInput('dry-run')

    const minAgeHours = Number(core.getInput('min-age-hours'))
    if (!Number.isFinite(minAgeHours) || minAgeHours < 0) {
      core.setFailed(
        `min-age-hours must be a non-negative number, got '${core.getInput('min-age-hours')}'`
      )
      return
    }

    const api = new GitHubApi(token)
    const registry = await RegistryClient.create(owner, packageName, token)

    const result = await prune(
      { owner, packageName, minAgeHours, dryRun },
      api,
      registry
    )

    core.setOutput('deleted', result.deleted)
    core.setOutput('kept', result.kept)
    core.setOutput('failed', result.failed)

    if (result.failed > 0) {
      core.setFailed(`failed to delete ${result.failed} version(s)`)
    }
  } catch (error) {
    // Fail the workflow run if an error occurs
    if (error instanceof Error) core.setFailed(error.message)
    else core.setFailed(String(error))
  }
}
