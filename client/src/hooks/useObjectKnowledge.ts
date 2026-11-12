import { useCallback, useEffect, useRef, useState } from 'react'
import type { TableInfo } from '../types/chat'
import type { ObjectKnowledgeResponse, KnowledgeMapping, KnowledgeTextPatch } from '../types/objectKnowledge'
import { sourceAccess, useSourceAccess, type SourceTarget } from '../contexts/SourceAccessContext'
import knowledgeBaseService, { KnowledgeRequestError } from '../services/knowledgeBaseService'

interface Snapshot { data: ObjectKnowledgeResponse; objects: TableInfo[]; target: SourceTarget; selectionRevision?: number; targetError?: string }
interface Edit { description?: string; fields: Record<string, string> }
type Edits = Record<string, Edit>
export function useObjectKnowledge(chatId: string) {
  const access = useSourceAccess(chatId)
  const [snapshot, setSnapshot] = useState<Snapshot>()
  const [pending, setPending] = useState<Snapshot>()
  const [edits, setEdits] = useState<Edits>({})
  const [mappings, setMappings] = useState<Record<number, { checked: boolean; objectId: string }>>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [conflicted, setConflicted] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)
  const version = useRef(0)
  const mounted = useRef(false)
  const busyRef = useRef(false)
  const observedV2 = useRef(false)
  const draftRef = useRef(edits)
  draftRef.current = edits
  const conflictRef = useRef(conflicted)
  conflictRef.current = conflicted

  const adopt = (next: Snapshot) => {
    setSnapshot(next); setPending(undefined); setEdits({}); setMappings({})
    setConflicted(false); setError(null); setSuccess(null)
  }
  const reload = useCallback(async () => {
    if (busyRef.current) return
    const request = ++version.current
    const authority = sourceAccess()
    let target: SourceTarget | undefined
    const current = () => mounted.current && request === version.current && (!target || authority.isCurrent(target))
    setLoading(true); setError(null); setSuccess(null)
    try {
      target = await authority.admit(chatId)
      authority.assert(target)
      const selectionRevision = authority.view(chatId).chat?.selection_revision
      const data = await knowledgeBaseService.getKnowledgeBase(chatId)
      let objects: TableInfo[] = []
      let targetError: string | undefined
      if (data.query_objects_enabled === true) {
        try { objects = await knowledgeBaseService.getSelectedObjects(chatId) }
        catch (reason) { targetError = reason instanceof Error ? reason.message : '映射目标加载失败，请重新加载。' }
      }
      if (!current()) return
      if (authority.view(chatId).chat?.selection_revision !== selectionRevision) throw new KnowledgeRequestError('对象选择已变化，请重新加载知识库。', true)
      if (data.query_objects_enabled === true) observedV2.current = true
      const next = { data, objects, targetError, target, selectionRevision }
      if (Object.keys(draftRef.current).length || conflictRef.current) setPending(next)
      else {
        setSnapshot(next); setPending(undefined); setMappings({}); setConflicted(false)
      }
    } catch (reason) {
      if (current()) {
        setError(reason instanceof Error ? reason.message : '知识库加载失败，请重试。')
        if (reason instanceof KnowledgeRequestError && reason.conflict) setConflicted(true)
      }
    } finally {
      if (mounted.current && request === version.current) setLoading(false)
    }
  }, [chatId])
  useEffect(() => {
    mounted.current = true
    void reload()
    return () => { mounted.current = false }
  }, [reload])

  const v2 = snapshot?.data.query_objects_enabled === true
  // A successful legacy GET may explicitly report false, including PostgreSQL.
  // Once v2 was observed in this editor, a later flag-off response cannot downgrade it.
  const legacy = !access.chat?.selected_objects && !v2 && !observedV2.current
  const scopeChanged = !!snapshot && (snapshot.selectionRevision !== access.chat?.selection_revision ||
    !access.coordinator?.isCurrent(snapshot.target))
  const currentObjects = scopeChanged ? [] : snapshot?.objects ?? []
  const capabilityError = scopeChanged ? '对象选择或数据源已变化；请重新加载知识库，本地草稿仍保留。' : snapshot && !legacy && (!v2 || !snapshot.data.kb_revision)
    ? '对象知识库功能未启用或版本信息不完整；原文仍保留，请恢复功能后重新加载。' : null
  const dirty = Object.keys(edits).length > 0
  const changeText = (key: string, base: string, value: string, field?: string) => {
    setSuccess(null)
    setEdits(previous => {
      const next = { ...previous }
      const edit: Edit = { ...next[key], fields: { ...next[key]?.fields } }
      if (field === undefined) { if (value === base) delete edit.description; else edit.description = value }
      else { if (value === base) delete edit.fields[field]; else edit.fields[field] = value }
      if (edit.description === undefined && !Object.keys(edit.fields).length) delete next[key]
      else next[key] = edit
      return next
    })
  }
  const getText = (key: string, base: string, field?: string) => field === undefined
    ? edits[key]?.description ?? base : edits[key]?.fields[field] ?? base

  const mutate = async (kind: 'text' | 'mapping') => {
    if (!snapshot || busyRef.current || loading || conflicted || pending || capabilityError) return
    const batch: KnowledgeMapping[] = Object.entries(mappings).filter(([, value]) => value.checked)
      .map(([index, value]) => ({ description_index: Number(index), object_id: value.objectId }))
    if (kind === 'mapping' && (dirty || !v2 || !batch.length || batch.some(mapping => !snapshot.objects.some(object => object.object_id === mapping.object_id)))) {
      setError(dirty ? '请先保存说明，再确认映射。' : '请勾选历史说明，并为每条选择当前可读对象。')
      return
    }
    if (kind === 'text' && !dirty) return
    const request = version.current
    const authority = sourceAccess()
    const current = () => mounted.current && request === version.current && authority.isCurrent(snapshot.target)
    busyRef.current = true; setBusy(true); setError(null); setSuccess(null)
    try {
      authority.assertCurrent(snapshot.target)
      if (authority.view(chatId).chat?.selection_revision !== snapshot.selectionRevision) throw new KnowledgeRequestError('对象选择已变化，请重新加载知识库。', true)
      let updated: ObjectKnowledgeResponse
      if (kind === 'mapping') {
        updated = await knowledgeBaseService.confirmMappings(chatId, { expected_kb_revision: snapshot.data.kb_revision!, mappings: batch })
      } else {
        const patches: KnowledgeTextPatch[] = v2 ? Object.entries(edits).map(([key, edit]) => {
          const history = key.startsWith('h:')
          const index = Number(key.slice(2))
          return {
            ...(history ? { description_index: index } : { object_id: key.slice(2) }),
            ...(edit.description === undefined ? {} : { description: edit.description }),
            ...(Object.keys(edit.fields).length ? { field_descriptions: Object.entries(edit.fields).map(([field_name, description]) => ({ field_name, description })) } : {}),
          }
        }) : snapshot.data.table_descriptions.map((row, index) => ({
          table_name: row.table_name,
          description: edits['h:' + index]?.description ?? row.description,
          field_descriptions: row.field_descriptions.map(field => ({
            field_name: field.field_name,
            description: edits['h:' + index]?.fields[field.field_name] ?? field.description,
          })),
        }))
        updated = await knowledgeBaseService.updateKnowledgeBase(chatId, {
          ...(v2 ? { expected_kb_revision: snapshot.data.kb_revision } : {}), table_descriptions: patches,
        })
      }
      if (!current()) return
      setSnapshot({ ...snapshot, data: updated }); setEdits({}); setMappings({})
      setSuccess(kind === 'text' ? '说明已保存' : '映射已保存')
    } catch (reason) {
      if (current()) {
        setError(reason instanceof Error ? reason.message : '保存失败；本地草稿仍保留。')
        if (reason instanceof KnowledgeRequestError && reason.conflict) setConflicted(true)
        if (reason instanceof KnowledgeRequestError && reason.saved) setSuccess('更改已写入；索引尚未确认，需要重新加载。')
      }
    } finally {
      busyRef.current = false
      if (mounted.current) setBusy(false)
    }
  }
  const effective = (snapshot?.data.effective_descriptions ?? []).filter(row => currentObjects.some(object =>
    object.object_id === row.object_id && (!row.object_instance_version || !object.object_instance_version || row.object_instance_version === object.object_instance_version)))
  const status = scopeChanged ? '知识库需重新加载。' : snapshot?.data.index_status === 'invalidation_failed'
    ? '索引失效处理失败；更改已保存，需要重新加载并刷新。' : snapshot?.data.refresh_required || snapshot?.data.index_status === 'stale'
    ? '索引待更新；需要刷新后才会用于检索。'
    : snapshot?.data.index_status === 'ready' ? '索引已就绪。'
      : snapshot?.data.index_status === 'building' ? '索引构建中；尚未就绪。'
        : snapshot?.data.index_status === 'failed' ? '索引构建失败；说明已保留，需要刷新。' : '索引未就绪。'
  return { snapshot: snapshot && { ...snapshot, objects: currentObjects }, pending, edits, dirty, mappings, loading, busy, conflicted, error, success, capabilityError, v2, effective, status,
    blocked: !snapshot || busy || loading || conflicted || !!pending || !!capabilityError,
    reload, adoptPending: () => { if (pending) adopt(pending) }, changeText, getText,
    chooseMapping: (index: number, change: { checked?: boolean; objectId?: string }) => setMappings(previous => ({ ...previous, [index]: { ...(previous[index] ?? { checked: false, objectId: '' }), ...change } })),
    save: () => mutate('text'), confirm: () => mutate('mapping'),
  }
}
