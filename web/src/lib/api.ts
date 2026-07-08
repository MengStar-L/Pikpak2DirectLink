// Typed HTTP client for the Pikpak2DirectLink backend. Every method mirrors a
// route registered in internal/app/server.go and cdk_handlers.go. Cookies
// (admin `session` / CDK `cdk`) flow automatically with same-origin requests.
import type {
  AccountSummary,
  AuthResult,
  AuthStatus,
  BatchSummary,
  CDKView,
  ConfigResponse,
  CreateCDKRequest,
  CreateJobRequest,
  Job,
  LogEntry,
  MergeCDKRequest,
  ResolveHistoryDetail,
  ResolveHistorySummary,
  SelectItemsRequest,
  SettingsResponse,
  UpdateCDKRequest,
  UpdateSettingsRequest,
  UpdateStatus,
  UserJobView,
  UserStatusResponse,
} from './types'
import type { ApiError } from './types'

let unauthorizedHandler: (() => void) | null = null

/** Install a callback fired once when any request returns 401. Each app wires
 * its own redirect (admin → "/", CDK user → "/u"). */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn
}

function makeError(message: string, status?: number): ApiError {
  const err = new Error(message) as ApiError
  err.status = status
  return err
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      credentials: 'same-origin',
    })
  } catch (e) {
    throw makeError(e instanceof Error ? e.message : '网络请求失败')
  }

  if (res.status === 401 && unauthorizedHandler) {
    unauthorizedHandler()
  }

  if (res.status === 204) {
    return undefined as T
  }

  let payload: any = null
  const text = await res.text()
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      // non-JSON body; keep payload null
    }
  }

  if (!res.ok) {
    const message = (payload && payload.error) || `请求失败 (${res.status})`
    throw makeError(message, res.status)
  }
  return payload as T
}

export const api = {
  auth: {
    status: () => request<AuthStatus>('GET', '/api/auth/status'),
    setup: (password: string) => request<AuthResult>('POST', '/api/auth/setup', { password }),
    login: (password: string) => request<AuthResult>('POST', '/api/auth/login', { password }),
    logout: () => request<AuthResult>('POST', '/api/auth/logout'),
    password: (current_password: string, new_password: string) =>
      request<AuthResult>('POST', '/api/auth/password', { current_password, new_password }),
  },

  config: () => request<ConfigResponse>('GET', '/api/config'),

  settings: {
    get: () => request<SettingsResponse>('GET', '/api/settings'),
    update: (body: UpdateSettingsRequest) => request<SettingsResponse>('PUT', '/api/settings', body),
  },

  update: {
    status: () => request<UpdateStatus>('GET', '/api/update'),
    check: () => request<UpdateStatus>('POST', '/api/update/check'),
    install: () => request<UpdateStatus>('POST', '/api/update/install'),
  },

  logs: {
    list: (after = 0) => request<{ logs: LogEntry[] }>('GET', `/api/logs?after=${after}`),
    clear: () => request<AuthResult>('DELETE', '/api/logs'),
  },

  accounts: {
    list: () => request<{ accounts: AccountSummary[] }>('GET', '/api/accounts'),
    add: (body: { username: string; password: string; traffic_limit_gb: number }) =>
      request<AccountSummary>('POST', '/api/accounts', body),
    update: (id: string, traffic_limit_gb: number) =>
      request<AuthResult>('PATCH', `/api/accounts/${id}`, { traffic_limit_gb }),
    remove: (id: string) => request<AuthResult>('DELETE', `/api/accounts/${id}`),
    reset: (id: string) => request<AuthResult>('POST', `/api/accounts/${id}/reset`),
    refreshLogin: (id: string) => request<AccountSummary>('POST', `/api/accounts/${id}/refresh-login`),
    deleteParseError: (id: string, index: number) =>
      request<AuthResult>('DELETE', `/api/accounts/${id}/parse-errors/${index}`),
  },

  jobs: {
    create: (body: CreateJobRequest) => request<Job>('POST', '/api/jobs', body),
    get: (id: string) => request<Job>('GET', `/api/jobs/${id}`),
    select: (id: string, body: SelectItemsRequest) => request<Job>('POST', `/api/jobs/${id}/select`, body),
  },

  cdks: {
    list: () => request<{ cdks: CDKView[] }>('GET', '/api/cdks'),
    create: (body: CreateCDKRequest) => request<{ cdks: CDKView[] }>('POST', '/api/cdks', body),
    deleteExpired: () => request<{ deleted: number }>('DELETE', '/api/cdks/expired'),
    update: (code: string, body: UpdateCDKRequest) =>
      request<CDKView>('PATCH', `/api/cdks/${encodeURIComponent(code)}`, body),
    remove: (code: string) => request<AuthResult>('DELETE', `/api/cdks/${encodeURIComponent(code)}`),
  },

  u: {
    login: (code: string) => request<UserStatusResponse>('POST', '/api/u/login', { code }),
    status: () => request<UserStatusResponse>('GET', '/api/u/status'),
    logout: () => request<AuthResult>('POST', '/api/u/logout'),
    mergeCDK: (primaryCode: string, secondaryCode: string) =>
      request<UserStatusResponse>('POST', '/api/u/cdks/merge', {
        primary_code: primaryCode,
        secondary_code: secondaryCode,
      } satisfies MergeCDKRequest),
    jobs: {
      create: (body: CreateJobRequest) => request<UserJobView>('POST', '/api/u/jobs', body),
      get: (id: string) => request<UserJobView>('GET', `/api/u/jobs/${id}`),
      select: (id: string, body: SelectItemsRequest) =>
        request<UserJobView>('POST', `/api/u/jobs/${id}/select`, body),
    },
    history: {
      list: () => request<{ history: ResolveHistorySummary[] }>('GET', '/api/u/history'),
      get: (id: string) => request<ResolveHistoryDetail>('GET', `/api/u/history/${encodeURIComponent(id)}`),
    },
  },
}

export type Batch = BatchSummary
export { type ApiError }
