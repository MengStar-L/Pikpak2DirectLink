// Type definitions mirroring the Go JSON contract in internal/app.
// Field names match the server's JSON tags exactly.

export type AuthStatus = {
  configured: boolean
  authenticated: boolean
}

export type ConfigResponse = {
  configured: boolean
  account_count: number
  failed_account_count: number
  available_account_count: number
  root_folder: string
  auth_required: boolean
  authenticated: boolean
  password_fixed: boolean
}

export type SettingsResponse = {
  concurrency: number
  max_concurrency: number
  parallel: boolean
  running: number
  waiting: number
  serial_timeout_seconds: number
  parallel_timeout_seconds: number
  task_timeout_seconds: number
  min_task_timeout_seconds: number
  max_task_timeout_seconds: number
}

export type UpdateSettingsRequest = {
  concurrency?: number
  task_timeout_seconds?: number
  task_timeout_minutes?: number
}

export type AccountStatus = 'available' | 'failed'

export type ParseError = {
  time: string
  job_id?: string
  message: string
}

export type AccountSummary = {
  id: string
  username: string
  status: AccountStatus
  ready: boolean
  logged_in: boolean
  persisted: boolean
  premium: boolean
  premium_type: string
  premium_until: string
  premium_error: string
  premium_checked_at: string
  traffic_limit: number
  traffic_used: number
  traffic_limited: boolean
  last_error: string
  last_failed_at: string
  credential_checked_at: string
  credential_next_check_at: string
  credential_check_error: string
  parse_error_count: number
  parse_errors?: ParseError[]
  created_at: string
  updated_at: string
}

export type DownloadItem = {
  id: string
  name: string
  path: string
  kind: string
  mime_type: string
  size: string
}

export type JobResult = {
  file: DownloadItem
  url: string
  direct_url: string
  proxy_url: string
  proxy_token: string
  expires_at: string
}

export type AccountAttempt = {
  account_id: string
  username: string
  status: string
  error: string
}

export type ShareState = {
  share_id: string
  tail_id: string
  pass_code_token: string
  selected_id: string
  selected_ids: string[]
}

export type BatchSummary = {
  total: number
  succeeded: number
  failed: number
  failures: { label: string; error: string }[]
}

export type JobKind = 'magnet' | 'share' | 'batch'
export type JobMode = 'direct' | 'proxy'
export type JobStatus = 'queued' | 'running' | 'selection_required' | 'completed' | 'failed'
export type JobStage = 'transfer' | 'source_selection' | 'result_selection' | 'complete' | 'failed'

export type Job = {
  id: string
  kind: JobKind
  mode: JobMode
  input: string
  pass_code: string
  status: JobStatus
  stage: JobStage
  message: string
  error: string
  account_id: string
  share: ShareState
  items: DownloadItem[]
  account_attempts: AccountAttempt[]
  result: JobResult
  results: JobResult[]
  warnings: string[]
  queue_ahead: number
  batch: BatchSummary
  created_at: string
  updated_at: string
}

export type CreateJobRequest = {
  input: string
  pass_code?: string
  mode?: JobMode
}

export type SelectItemsRequest = {
  item_ids?: string[]
  item_id?: string
}

export type UpdatePhase =
  | 'idle'
  | 'checking'
  | 'up_to_date'
  | 'available'
  | 'downloading'
  | 'verifying'
  | 'installing'
  | 'restarting'
  | 'error'

export type UpdateStatus = {
  phase: UpdatePhase
  current_version: string
  latest_version: string
  update_available: boolean
  downloaded_bytes: number
  total_bytes: number
  progress: number
  message: string
  error: string
  release_notes: string
  release_url: string
  asset_name: string
  checked_at: string
  repo: string
  platform: string
  managed: boolean
}

export type LogLevel = 'info' | 'success' | 'warn' | 'error'

export type LogEntry = {
  id: number
  time: string
  level: LogLevel
  job_id: string
  message: string
  details: string[]
}

export type CDKView = {
  code: string
  remaining_bytes: number
  used_bytes: number
  remaining_label: string
  used_label: string
  expires_at: string
  created_at: string
  days_left: number
  expired: boolean
  allow_proxy: boolean
}

export type CreateCDKRequest = {
  count: number
  traffic_gb: number
  days: number
  allow_proxy: boolean
}

export type UpdateCDKRequest = {
  traffic_gb: number
  days: number
  allow_proxy: boolean
}

export type MergeCDKRequest = {
  primary_code: string
  secondary_code: string
}

// CDK user-portal projections.
export type UserStatusResponse = CDKView & {
  queue: { waiting: number; active: boolean }
}

export type UserJobView = {
  id: string
  kind: JobKind
  mode: JobMode
  status: JobStatus
  stage: JobStage
  message: string
  error: string
  items: DownloadItem[]
  result: JobResult
  results: JobResult[]
  batch: BatchSummary
  warnings: string[]
  queue_ahead: number
  created_at: string
  updated_at: string
}

export type ResolveHistorySummary = {
  id: string
  job_id: string
  kind: JobKind
  mode: JobMode
  input: string
  result_count: number
  batch?: BatchSummary
  created_at: string
  completed_at: string
  expires_at: string
}

export type ResolveHistoryDetail = ResolveHistorySummary & {
  results: JobResult[]
}

export type QueueView = { waiting: number; active: boolean }

export type AuthResult = { status: string }

export type ApiError = Error & { status?: number }
