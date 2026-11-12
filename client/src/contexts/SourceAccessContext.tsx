import { createContext, useContext, useSyncExternalStore } from 'react'
import type { Chat } from '../types/chat'
import type { DataSyncStatus } from '../types/dataSync'
import { dataSyncService } from '../services/dataSyncService'
import { zhCN } from '../i18n/zh-CN'

export class SourceAccessError extends Error {
  constructor(message = zhCN.sourceAccess.unavailable) { super(message); this.name = 'SourceAccessError' }
}

/** Backend admission can lose the race after the last lightweight status read. */
export function isSourceAccessDenial(error: unknown): boolean {
  if (error instanceof SourceAccessError) return true
  if (!error || typeof error !== 'object' || !('response' in error)) return false
  const response = error.response
  if (!response || typeof response !== 'object') return false
  if ('status' in response && response.status === 423) return true
  if (!('data' in response) || !response.data || typeof response.data !== 'object') return false
  const body = response.data
  const data = 'data' in body && body.data && typeof body.data === 'object' ? body.data : body
  return 'code' in data && typeof data.code === 'string' && /^(DATA_SYNC_|SOURCE_(UNRESOLVED|STATE_UNAVAILABLE|BINDING_CONFLICT))/.test(data.code)
}

export interface SourceTarget {
  instance: object
  owner: string
  session: number
  selection: number
  chatId?: string
  binding?: string
  admission: number
}
interface Entry { chat: Chat; binding: string; admission: number; status?: DataSyncStatus; reason: string }
export interface SourceAccessView { allowed: boolean; reason: string; chat?: Chat; status?: DataSyncStatus }
const unavailable: SourceAccessView = { allowed: false, reason: zhCN.sourceAccess.unavailable }

function bindingOf(chat: Chat): string {
  const c = chat.connection
  return JSON.stringify([chat.user_id, chat.source_classification, chat.source_id, chat.source_binding_revision,
    c.type, c.host, c.port, c.database, c.username, c.ssh_enabled, c.ssh_host, c.ssh_port, c.ssh_username,
    c.use_ssl, c.ssl_mode, c.ssl_cert_url, c.ssl_key_url, c.ssl_root_cert_url])
}

/** One App-owned authority. The service bridge points to this exact instance. */
export class SourceAccessCoordinator {
  private readonly identity = {}
  private owner = ''
  private session = 0
  private selection = 0
  private selected?: string
  private entries = new Map<string, Entry>()
  private inputs = new Map<string, Chat>()
  private revisions = new Map<string, number>()
  private listeners = new Set<() => void>()
  private version = 0
  private timer?: number
  private request?: AbortController
  private serial: Promise<void> = Promise.resolve()
  private pending = new Map<string, Promise<void>>()
  private running = false
  private discovery = 0
  private visible = true
  private online = true

  constructor(private transport = dataSyncService) {}
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  snapshot = () => this.version
  private emit() { this.version++; this.listeners.forEach(listener => listener()) }
  configure(owner: string, selected: Chat | undefined, chats: Chat[]) {
    if (owner !== this.owner) {
      this.owner = owner; this.session++; this.selection++
      this.entries.clear(); this.inputs.clear(); this.revisions.clear()
      this.request?.abort(); this.clearTimer()
    }
    for (const chat of chats) {
      if (this.inputs.get(chat.id) !== chat) { this.inputs.set(chat.id, chat); this.register(chat) }
    }
    if (selected && this.inputs.get(selected.id) !== selected) {
      this.inputs.set(selected.id, selected); this.register(selected)
    }
    if (this.selected !== selected?.id) this.select(selected?.id)
    this.emit()
    this.discover()
  }
  select(chatId?: string) {
    if (this.selected === chatId) return
    this.selected = chatId; this.selection++
    this.request?.abort(); this.clearTimer()
    this.invalidateERP()
    this.emit()
    this.discover()
  }
  register(chat: Chat) {
    if (!this.owner || chat.user_id !== this.owner) return false
    const binding = bindingOf(chat)
    const old = this.entries.get(chat.id)
    if (old && (chat.source_binding_revision ?? 0) < (old.chat.source_binding_revision ?? 0)) return false
    // A delayed chat response (or stale parent render) cannot erase a saved v2 scope.
    if (old?.chat.selected_objects && (
      (chat.selection_revision ?? -1) < (old.chat.selection_revision ?? 0) ||
      !chat.selected_objects
    )) return false
    if (old?.binding === binding) { old.chat = chat; return true }
    this.entries.set(chat.id, { chat, binding, admission: (old?.admission ?? 0) + 1,
      reason: chat.source_classification === 'independent' ? '' : zhCN.sourceAccess.unavailable })
    this.emit()
    return true
  }
  capture(chatId?: string): SourceTarget {
    return { instance: this.identity, owner: this.owner, session: this.session, selection: this.selection, chatId,
      binding: chatId ? this.entries.get(chatId)?.binding : undefined,
      admission: chatId ? this.entries.get(chatId)?.admission ?? 0 : 0 }
  }
  isOwnerCurrent(target: SourceTarget): boolean {
    return target.instance === this.identity && target.owner === this.owner && target.session === this.session
  }
  isCurrent(target: SourceTarget): boolean {
    return !!this.owner && this.isOwnerCurrent(target)
      && target.selection === this.selection && (!target.chatId || target.binding === this.entries.get(target.chatId)?.binding)
  }
  assertCurrent(target: SourceTarget) {
    if (!this.isCurrent(target)) throw new SourceAccessError(zhCN.sourceAccess.changed)
  }
  assert(target: SourceTarget) {
    this.assertCurrent(target)
    const entry = target.chatId ? this.entries.get(target.chatId) : undefined
    const view = this.view(target.chatId)
    if (!view.allowed || entry?.admission !== target.admission) throw new SourceAccessError(view.reason || zhCN.sourceAccess.changed)
  }
  view(chatId = this.selected): SourceAccessView {
    const entry = chatId ? this.entries.get(chatId) : undefined
    if (!this.owner || !entry || entry.chat.user_id !== this.owner) return unavailable
    if (entry.chat.source_classification === 'independent') return { allowed: true, reason: '', chat: entry.chat }
    return { allowed: entry.reason === '' && this.visible && this.online, reason: entry.reason || (!this.visible || !this.online ? zhCN.sourceAccess.unavailable : ''), chat: entry.chat, status: entry.status }
  }
  /** Metadata discovery may grant independent authority; missing metadata never does. */
  async admit(chatId: string): Promise<SourceTarget> {
    const initial = this.capture(chatId)
    const entry = this.entries.get(chatId)
    if (!entry || !entry.chat.source_classification || (entry.chat.source_classification === 'registered_erp' && !entry.status)) {
      await this.revalidate(chatId)
      // Discovery can establish the first binding. Account/selection still must match.
      if (initial.owner !== this.owner || initial.session !== this.session || initial.selection !== this.selection) throw new SourceAccessError(zhCN.sourceAccess.changed)
      if (initial.binding && entry?.chat.source_classification && initial.binding !== this.entries.get(chatId)?.binding) throw new SourceAccessError(zhCN.sourceAccess.changed)
    }
    const target = this.capture(chatId)
    this.assert(target)
    return target
  }
  /** Sync metadata shares discovery's IO queue, but never consumes question admission. */
  readSyncStatus(target: SourceTarget, signal?: AbortSignal, requestId?: string): Promise<DataSyncStatus> {
    const operation = this.serial.then(async () => {
      this.assertCurrent(target)
      if (!target.chatId || signal?.aborted) throw new SourceAccessError()
      const status = await this.transport.getStatus(target.chatId, signal, requestId)
      this.assertCurrent(target)
      if (signal?.aborted) throw new SourceAccessError()
      return status
    })
    this.serial = operation.then(() => {}, () => {})
    return operation
  }
  private clearTimer() { clearTimeout(this.timer); this.timer = undefined; this.discovery++ }
  private invalidateERP() {
    for (const entry of this.entries.values()) {
      if (entry.chat.source_classification !== 'independent') {
        entry.admission++; entry.status = undefined; entry.reason = zhCN.sourceAccess.unavailable
      }
    }
  }
  private activity = () => {
    this.visible = document.visibilityState !== 'hidden'
    this.online = navigator.onLine !== false
    this.selection++ // Pending continuations from before hide/offline cannot resume later.
    this.request?.abort(); this.clearTimer(); this.invalidateERP(); this.emit()
    this.discover()
  }
  start() {
    this.running = true
    this.visible = document.visibilityState !== 'hidden'; this.online = navigator.onLine !== false
    document.addEventListener('visibilitychange', this.activity)
    window.addEventListener('online', this.activity); window.addEventListener('offline', this.activity)
    this.discover()
    return () => {
      this.running = false; this.session++; this.request?.abort(); this.clearTimer()
      document.removeEventListener('visibilitychange', this.activity)
      window.removeEventListener('online', this.activity); window.removeEventListener('offline', this.activity)
    }
  }
  private discover() {
    this.clearTimer()
    const discovery = this.discovery
    const id = this.selected
    if (!this.running || !this.owner || !id || !this.visible || !this.online || this.entries.get(id)?.chat.source_classification === 'independent') return
    void this.revalidate(id).finally(() => {
      if (discovery === this.discovery && this.running && this.selected === id && this.visible && this.online) this.timer = window.setTimeout(() => this.discover(), 2000)
    })
  }
  /** All discovery/status IO is serialized, including aborted requests and target switches. */
  revalidate(chatId: string): Promise<void> {
    const target = this.capture(chatId)
    const key = JSON.stringify([target.session, target.selection, chatId, target.binding])
    const existing = this.pending.get(key)
    if (existing) return existing
    const operation = this.serial.then(async () => {
      if (!this.isCurrent(target) || !this.visible || !this.online) return
      const controller = new AbortController()
      this.request = controller
      try {
        let entry = this.entries.get(chatId)
        if (!entry || entry.chat.source_classification !== 'registered_erp' || !entry.chat.source_id || !entry.chat.source_binding_revision) {
          const chat = await this.transport.getChat(chatId, controller.signal)
          if (!this.isCurrent(target) || controller.signal.aborted || chat.id !== chatId || chat.user_id !== this.owner) return
          this.register(chat)
          entry = this.entries.get(chatId)
        }
        if (!entry) return
        if (entry.chat.source_classification === 'independent') return
        if (entry.chat.source_classification !== 'registered_erp' || !entry.chat.source_id || !entry.chat.source_binding_revision) {
          entry.reason = zhCN.sourceAccess.unresolved; entry.admission++; this.emit(); return
        }
        const binding = entry.binding
        const status = await this.transport.getStatus(chatId, controller.signal)
        if (controller.signal.aborted || target.session !== this.session || target.selection !== this.selection || this.entries.get(chatId)?.binding !== binding) return
        if (status.schema_version !== 1 || status.source_id !== entry.chat.source_id || status.binding_revision !== entry.chat.source_binding_revision) {
          entry.status = undefined; entry.reason = zhCN.sourceAccess.unavailable; entry.admission++
          // Refresh metadata on the next discovery; never adopt the status binding as a Chat binding.
          const chat = await this.transport.getChat(chatId, controller.signal)
          if (this.isCurrent({ ...target, binding }) && !controller.signal.aborted && chat.id === chatId) this.register(chat)
          this.emit(); return
        }
        const previous = this.revisions.get(status.source_id) ?? -1
        if (!Number.isSafeInteger(status.source_revision) || status.source_revision < previous) {
          entry.reason = zhCN.sourceAccess.unavailable; entry.admission++; this.emit(); return
        }
        this.revisions.set(status.source_id, status.source_revision)
        const reason = status.ask_allowed === true && status.maintenance_state === 'idle' ? ''
          : status.maintenance_state === 'syncing' || status.block_reason === 'DATA_SYNC_IN_PROGRESS' ? zhCN.sourceAccess.syncing
          : status.maintenance_state === 'recovering' || status.block_reason === 'DATA_SYNC_RECOVERING' ? zhCN.sourceAccess.recovering
          : zhCN.sourceAccess.unavailable
        // A source transition invalidates pending work even if it later becomes idle again.
        for (const related of this.entries.values()) {
          if (related.chat.source_id === status.source_id && related.chat.source_classification !== 'independent') {
            if (status.source_revision > previous || (reason && related.reason !== reason)) related.admission++
            if (related === entry) { related.status = status; related.reason = reason }
            else if (reason || status.source_revision > previous) { related.reason = reason || zhCN.sourceAccess.unavailable; related.status = undefined }
          }
        }
        this.emit()
      } catch {
        if (this.isCurrent(target) && !controller.signal.aborted) {
          const entry = this.entries.get(chatId)
          if (entry && entry.chat.source_classification !== 'independent') {
            entry.reason = zhCN.sourceAccess.unavailable; entry.admission++; entry.status = undefined; this.emit()
          }
        }
      } finally { if (this.request === controller) this.request = undefined }
    })
    this.serial = operation.catch(() => {})
    this.pending.set(key, operation)
    void operation.finally(() => { this.pending.delete(key) })
    return operation
  }
}

export const SourceAccessContext = createContext<SourceAccessCoordinator | null>(null)
let appCoordinator: SourceAccessCoordinator | null = null
export function installSourceAccess(coordinator: SourceAccessCoordinator) {
  appCoordinator = coordinator
  return () => { if (appCoordinator === coordinator) appCoordinator = null }
}
export function sourceAccess(): SourceAccessCoordinator {
  if (!appCoordinator) throw new SourceAccessError()
  return appCoordinator
}
export function useSourceAccess(chatId?: string) {
  const coordinator = useContext(SourceAccessContext)
  useSyncExternalStore(coordinator?.subscribe ?? noSubscribe, coordinator?.snapshot ?? zero, zero)
  return { ...(coordinator?.view(chatId) ?? unavailable), coordinator }
}
const zero = () => 0
const noSubscribe = () => () => {}
