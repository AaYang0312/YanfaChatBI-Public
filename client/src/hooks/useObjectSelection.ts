import { useCallback, useEffect, useRef, useState } from 'react'
import type { Chat, ObjectSelection, QueryObjectKind, TableInfo, TablesResponse } from '../types/chat'
import chatService from '../services/chatService'
import { sourceAccess, type SourceTarget } from '../contexts/SourceAccessContext'

export const objectKey = (object: TableInfo) => object.object_id ?? object.name
const refOf = (object: TableInfo) => ({ schema: object.schema_name!, name: object.object_name!, kind: object.object_type! })
const matches = (object: TableInfo, selection: ObjectSelection) => selection.mode === 'all'
  ? selection.include_kinds?.includes(object.object_type!)
  : selection.objects?.some(ref => ref.schema === object.schema_name && ref.name === object.object_name && ref.kind === object.object_type)

export function indexStatus(chat?: Chat): string {
  const index = chat?.query_object_index
  if (!index) return '索引未构建'
  if (index.status === 'ready' && index.format_version === 2 && index.active_generation) return '索引已就绪'
  if (index.status === 'building') return '索引构建中'
  if (index.status === 'failed') return '索引构建失败，请刷新结构后重试'
  return '索引待更新'
}

interface Draft {
  directory: TablesResponse
  selected: string[]
  allKinds: QueryObjectKind[] | null
  v2: boolean
  chat: Chat
  target: SourceTarget
}
export function useObjectSelection(chat: Chat | undefined, enabled = true) {
  const [draft, setDraft] = useState<Draft>()
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [blocked, setBlocked] = useState(true)
  const [success, setSuccess] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const latestChat = useRef(chat)
  latestChat.current = chat
  const sequence = useRef(0)
  const savingRef = useRef(false)
  const mounted = useRef(false)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const reload = useCallback(async () => {
    const current = latestChat.current
    if (!current) return
    const request = ++sequence.current
    const authority = sourceAccess()
    const initial = authority.capture(current.id)
    const isCurrent = () => request === sequence.current && authority.isCurrent(initial)
    setLoading(true); setBlocked(true); setError(null); setSuccess(null); setDraft(undefined)
    try {
      const target = await authority.admit(current.id)
      if (request !== sequence.current) return
      authority.assert(target)
      // Observing the catalog can invalidate the manifest without changing selection_revision.
      const directory = await chatService.getTables(current.id)
      authority.assertCurrent(target)
      if (request !== sequence.current) return
      const metadata = await chatService.getChat(current.id)
      authority.assertCurrent(target)
      if (request !== sequence.current) return
      const selectedObjects = directory.selected_objects ?? metadata.selected_objects
      const v2 = metadata.connection.type === 'postgresql' && directory.metadata_version === 2 && directory.query_objects_enabled === true
      if ((selectedObjects || (metadata.connection.type === 'postgresql' && directory.metadata_version === 2)) && !v2) throw new Error('对象功能未启用或服务端不兼容；保留原对象选择，请恢复功能后重新加载对象。')
      if (v2 && metadata.selection_revision !== undefined && metadata.selection_revision !== directory.selection_revision) {
        throw new Error('对象目录与选择版本不一致；保留原选择，请重新加载对象。')
      }
      if (v2 && (directory.selection_revision === undefined || directory.tables.some(object =>
        !object.object_id || !object.schema_name || !object.object_name || !['table', 'view'].includes(object.object_type ?? '')))) {
        throw new Error('对象目录信息不完整；保留原选择，请重新加载对象。')
      }
      const allKinds: QueryObjectKind[] | null = selectedObjects?.mode === 'all'
        ? selectedObjects.include_kinds ?? null
        : selectedObjects ? null : (!metadata.selected_collections || metadata.selected_collections === 'ALL') ? ['table'] : null
      const selected = directory.tables.filter(object => selectedObjects
        ? matches(object, selectedObjects)
        : allKinds ? object.object_type !== 'view' : object.is_selected).map(objectKey)
      if (selectedObjects?.mode === 'explicit' && selectedObjects.objects?.some(ref =>
        !directory.tables.some(object => object.schema_name === ref.schema && object.object_name === ref.name && object.object_type === ref.kind))) {
        throw new Error('部分已选对象已失效或无读取权限；保留原选择，请恢复权限后重新加载对象。')
      }
      if (!selectedObjects && metadata.selected_collections && metadata.selected_collections !== 'ALL' &&
          selected.length !== new Set(metadata.selected_collections.split(',').map(name => name.trim())).size) {
        throw new Error('部分已选对象不可用；保留原选择，请恢复权限后重新加载对象。')
      }
      // Old servers can return only the still-readable subset; never reinterpret that as ALL.
      if (!v2 && metadata.selected_collections && metadata.selected_collections !== 'ALL' &&
          metadata.selected_collections.split(',').some(name => !directory.tables.some(object => object.name === name.trim()))) {
        throw new Error('部分已选表不可用；保留原选择，请重新加载对象。')
      }
      setDraft({ directory, selected, allKinds, v2, chat: metadata.selection_revision === directory.selection_revision
        ? metadata : { ...metadata, query_object_index: null }, target })
      setBlocked(false)
    } catch (reason) {
      if (isCurrent()) setError(reason instanceof Error ? reason.message : '对象加载失败，请重试。')
    } finally {
      if (request === sequence.current) setLoading(false)
    }
  }, [])

  const invalidate = useCallback(() => { sequence.current++ }, [])
  const id = chat?.id
  const revision = chat?.source_binding_revision
  useEffect(() => {
    if (enabled && id) void reload()
    return invalidate
  }, [enabled, id, revision, reload, invalidate])

  const toggle = (key: string) => {
    setError(null); setSuccess(null)
    setDraft(previous => previous && ({ ...previous, allKinds: null, selected: previous.selected.includes(key)
      ? previous.selected.filter(value => value !== key) : [...previous.selected, key] }))
  }
  const chooseAll = (kinds: QueryObjectKind[]) => {
    setError(null); setSuccess(null)
    setDraft(previous => previous && ({ ...previous, allKinds: kinds, selected: previous.directory.tables
      .filter(object => kinds.includes(object.object_type ?? 'table')).map(objectKey) }))
  }
  const save = async (legacySave?: (id: string, value: string) => Promise<void>): Promise<Chat | undefined> => {
    if (!draft || blocked || loading || savingRef.current) return
    if (!draft.selected.length) { setError('至少需要选择一个查询对象。'); return }
    const request = sequence.current
    const authority = sourceAccess()
    savingRef.current = true; setSaving(true); setError(null); setSuccess(null)
    try {
      authority.assertCurrent(draft.target)
      const target = await authority.admit(draft.chat.id)
      authority.assert(target)
      let updated: Chat
      if (draft.v2) {
        const selection: ObjectSelection = draft.allKinds
          ? { selection_version: 2, mode: 'all', objects: [], include_kinds: draft.allKinds }
          : { selection_version: 2, mode: 'explicit', objects: draft.directory.tables.filter(object => draft.selected.includes(objectKey(object))).map(refOf) }
        updated = await chatService.updateSelectedObjects(draft.chat.id, selection, draft.directory.selection_revision!)
      } else {
        if (draft.chat.selected_objects) throw new Error('保留原对象选择，请恢复对象功能后重试。')
        const selection = draft.allKinds ? 'ALL' : draft.directory.tables.filter(object => draft.selected.includes(objectKey(object))).map(object => object.name).join(',')
        if (legacySave) { await legacySave(draft.chat.id, selection); updated = { ...draft.chat, selected_collections: selection } }
        else updated = await chatService.updateSelectedCollections(draft.chat.id, selection)
      }
      authority.assertCurrent(target)
      if (request !== sequence.current) return
      setDraft(previous => previous && ({ ...previous, chat: updated, directory: { ...previous.directory, selection_revision: updated.selection_revision ?? previous.directory.selection_revision, selected_objects: updated.selected_objects } }))
      setSuccess(draft.v2 ? '选择已保存；' + indexStatus(updated) + '。' : '选择已保存。')
      return updated
    } catch (reason) {
      if (request === sequence.current && authority.isCurrent(draft.target)) {
        setError(reason instanceof Error ? reason.message : '保存失败，请重新加载对象。')
      }
    } finally {
      savingRef.current = false
      // Directory reload/tab invalidation must not retain a finished save's busy state.
      // Result application above still requires both the request token and source authority.
      if (mounted.current) setSaving(false)
    }
  }
  return {
    tables: draft?.directory.tables ?? [], selected: draft?.selected ?? [], allKinds: draft?.allKinds,
    v2: draft?.v2 ?? false, loading, saving, error, success, search, setSearch, expanded,
    toggleExpanded: (key: string) => setExpanded(previous => ({ ...previous, [key]: !previous[key] })),
    toggle, chooseAll, reload, save, blocked, status: draft ? indexStatus(draft.chat) : '索引状态待确认',
  }
}
