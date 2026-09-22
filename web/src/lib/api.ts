import { adminApi } from './api-admin'
import { coreApi } from './api-core'

export { APIError, buildURL } from './http'
export type { Query } from './http'

/**
 * api is the single HTTP client used by the UI: coreApi (bootstrap, auth,
 * monitors, notifications) merged with adminApi (status pages, cluster,
 * settings, security). Every endpoint of the Go backend is declared here with
 * its TypeScript types.
 */
export const api = { ...coreApi, ...adminApi }
