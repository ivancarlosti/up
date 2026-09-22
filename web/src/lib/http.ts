/** APIError carries the stable backend error code so the UI can translate it. */
export class APIError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: number,
  ) {
    super(message)
    this.name = 'APIError'
  }
}

export type Query = Record<string, string | number | boolean | undefined | null>

export function buildURL(path: string, query?: Query): string {
  if (!query) return path
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    search.append(key, String(value))
  }
  const suffix = search.toString()
  return suffix ? `${path}?${suffix}` : path
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return { message: text }
  }
}

/**
 * request performs a JSON fetch against the Up API. It always sends the session
 * cookie (credentials: 'include') and converts an error response into an
 * APIError carrying the backend code.
 */
export async function request<T>(
  method: string,
  path: string,
  options: { body?: unknown; query?: Query } = {},
): Promise<T> {
  const response = await fetch(buildURL(path, options.query), {
    method,
    credentials: 'include',
    headers: options.body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  })

  if (response.status === 204) return undefined as T

  const text = await response.text()
  const payload = text ? safeJSON(text) : undefined

  if (!response.ok) {
    const details = payload as { code?: string; message?: string } | undefined
    throw new APIError(details?.code ?? 'ERR_INTERNAL', details?.message ?? response.statusText, response.status)
  }
  return payload as T
}

export const get = <T>(path: string, query?: Query) => request<T>('GET', path, { query })
export const post = <T>(path: string, body?: unknown) => request<T>('POST', path, { body })
export const put = <T>(path: string, body?: unknown) => request<T>('PUT', path, { body })
export const del = <T>(path: string) => request<T>('DELETE', path)
