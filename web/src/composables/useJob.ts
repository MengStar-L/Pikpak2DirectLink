// Shared resolve state machine: submit → poll GET → handle selection_required
// (pause and expose items) → select → resume → terminal. Generic over the job
// projection so admin (Job) and CDK user (UserJobView) reuse the same logic.
import { ref, computed, onUnmounted, type Ref } from 'vue'
import type {
  BatchSummary,
  CreateJobRequest,
  DownloadItem,
  JobMode,
  JobResult,
  JobStatus,
  SelectItemsRequest,
} from '../lib/types'

interface JobLike {
  id: string
  status: JobStatus
  message: string
  error: string
  items: DownloadItem[]
  result: JobResult
  results: JobResult[]
  batch: BatchSummary
  queue_ahead: number
}

export interface JobAdapter<T extends JobLike> {
  create: (body: CreateJobRequest) => Promise<T>
  get: (id: string) => Promise<T>
  select: (id: string, body: SelectItemsRequest) => Promise<T>
}

const POLL_MS = 1200
const TERMINAL: JobStatus[] = ['completed', 'failed']

export function useJob<T extends JobLike>(adapter: JobAdapter<T>) {
  const job = ref<T | null>(null) as Ref<T | null>
  const submitting = ref(false)
  const error = ref('')
  let timer: number | undefined

  function clearTimer() {
    if (timer) {
      window.clearTimeout(timer)
      timer = undefined
    }
  }

  function schedulePoll(id: string) {
    clearTimer()
    timer = window.setTimeout(async () => {
      try {
        const j = await adapter.get(id)
        job.value = j
        handle(j)
      } catch (e: any) {
        error.value = e?.message || '查询任务失败'
      }
    }, POLL_MS)
  }

  function handle(j: T) {
    if (j.status === 'selection_required' || TERMINAL.includes(j.status)) {
      clearTimer()
      return
    }
    schedulePoll(j.id)
  }

  async function submit(input: string, passCode: string, mode: JobMode) {
    clearTimer()
    error.value = ''
    submitting.value = true
    job.value = null
    try {
      const j = await adapter.create({ input, pass_code: passCode || undefined, mode })
      job.value = j
      handle(j)
    } catch (e: any) {
      error.value = e?.message || '提交解析失败'
    } finally {
      submitting.value = false
    }
  }

  async function selectItems(ids: string[]) {
    if (!job.value) return
    const id = job.value.id
    clearTimer()
    error.value = ''
    submitting.value = true
    try {
      const j = await adapter.select(id, { item_ids: ids })
      job.value = j
      handle(j)
    } catch (e: any) {
      error.value = e?.message || '选择失败'
    } finally {
      submitting.value = false
    }
  }

  function reset() {
    clearTimer()
    job.value = null
    error.value = ''
    submitting.value = false
  }

  onUnmounted(clearTimer)

  const phase = computed(() => {
    if (submitting.value) return 'submitting'
    if (!job.value) return 'idle'
    return job.value.status
  })

  return { job, phase, error, submitting, submit, selectItems, reset }
}
