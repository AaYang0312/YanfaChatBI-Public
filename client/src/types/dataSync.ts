export type DataSyncJobStatus = 'created' | 'running' | 'succeeded' | 'failed' | 'interrupted'

export interface DataSyncJob {
  schema_version: number
  job_id: string
  source_id: string
  request_id?: string
  initiating_chat_id?: string
  status: DataSyncJobStatus
  stage: 'preparing' | 'syncing' | 'verifying'
  revision: number
  created_at: string
  started_at: string | null
  finished_at: string | null
  cutoff: string | null
  expected_count: number | null
  processed_count: number
  succeeded_count: number
  failed_count: number
  shop_results: {
    shop_id: string
    status: string
    order_source: string
    orders_count: number
    aftersales_occurrence_count: number
    related_orders_refetch_completed: boolean
    safe_error_code: string
  }[] | null
  verification_passed: boolean
  safe_error_code: string
  safe_summary: string
  partial_commits: string
  release_confirmed: boolean
}

export interface DataSyncStatus {
  schema_version: number
  source_id: string
  source_revision: number
  binding_revision: number
  display_name: string
  readiness: { state: string; reasons: string[] | null }
  can_start: boolean
  ask_allowed: boolean
  block_reason: string
  maintenance_state: 'idle' | 'syncing' | 'recovering'
  operation_kind?: 'sync' | 'external_maintenance'
  maintenance_operation_ref?: string
  active_job: DataSyncJob | null
  latest_successful_sync_at: string | null
  request_match?: 'found' | 'not_found'
  matched_job?: DataSyncJob | null
}
