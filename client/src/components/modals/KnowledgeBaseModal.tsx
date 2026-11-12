import { useState } from 'react'
import { BookOpen, Loader2, X } from 'lucide-react'
import { useObjectKnowledge } from '../../hooks/useObjectKnowledge'
import { useSourceAccess } from '../../hooks/useSourceAccess'
import type { KnowledgeProvenance } from '../../types/knowledgeBase'

interface KnowledgeBaseModalProps { chatId: string; onClose: () => void }
const provenance = (value?: KnowledgeProvenance) => ({
  legacy_unknown: '旧说明：来源未知', database_comment: '数据库注释', user_edit: '人工编辑', ai_generated: 'AI 生成说明',
})[value ?? 'legacy_unknown'] ?? '旧说明：来源未知'

// A new chat gets a new editor instance; pending continuations cannot carry draft text across chats.
export default function KnowledgeBaseModal(props: KnowledgeBaseModalProps) {
  const access = useSourceAccess(props.chatId)
  const target = access.coordinator?.capture(props.chatId)
  return <KnowledgeBaseEditor key={JSON.stringify([props.chatId, target?.session, target?.binding])} {...props} />
}
function KnowledgeBaseEditor({ chatId, onClose }: KnowledgeBaseModalProps) {
  const kb = useObjectKnowledge(chatId)
  const access = useSourceAccess(chatId)
  const [confirmClose, setConfirmClose] = useState(false)
  const disabled = kb.blocked || !access.allowed
  const rows = kb.snapshot?.data.table_descriptions ?? []
  const objects = kb.snapshot?.objects ?? []
  const close = () => { if (kb.dirty) setConfirmClose(true); else onClose() }
  return <div className="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center p-4 z-[210]">
    <div role="dialog" aria-modal="true" aria-label="知识库说明" className="bg-white neo-border rounded-lg w-full max-w-3xl max-h-[90vh] flex flex-col overflow-hidden">
      <div className="flex justify-between items-center p-6 border-b-4 border-black">
        <h2 className="font-bold text-2xl flex items-center gap-2"><BookOpen className="w-6 h-6" />知识库说明</h2>
        <button type="button" aria-label="关闭知识库" onClick={close} disabled={kb.busy} className="p-2 rounded-lg hover:bg-gray-100"><X /></button>
      </div>
      <div className="p-6 overflow-y-auto space-y-5">
        <div className="flex items-center justify-between gap-2">
          <p className="text-sm text-gray-600">原始说明与当前有效说明分别保留；映射需要明确确认。</p>
          <button type="button" className="neo-button-secondary shrink-0" onClick={() => void kb.reload()} disabled={kb.loading || kb.busy || !access.allowed}>重新加载知识库</button>
        </div>
        {!access.allowed && <p role="alert" className="text-red-700">{access.reason}</p>}
        {(kb.error || kb.capabilityError) && <p role="alert" className="p-3 bg-red-50 border-2 border-red-500 rounded-lg">{kb.error || kb.capabilityError}</p>}
        {kb.snapshot?.targetError && <p role="alert" className="p-3 bg-amber-50 border-2 border-amber-500 rounded-lg">{kb.snapshot.targetError} 历史说明仍可编辑。</p>}
        {kb.success && <p role="status" className="p-3 bg-green-50 border-2 border-green-500 rounded-lg">{kb.success}</p>}
        {kb.snapshot && <p role="status" className="text-sm text-gray-600">{kb.status}</p>}
        {kb.loading && <p className="flex items-center gap-2"><Loader2 className="w-5 h-5 animate-spin" />正在加载知识库…</p>}
        {kb.pending && <div className="p-4 bg-amber-50 border-2 border-amber-500 rounded-lg space-y-2">
          <p>已获取服务器新版本。本地草稿尚未替换；请先复制需要保留的文字。</p>
          <details><summary className="cursor-pointer">查看服务器版本</summary>
            {kb.pending.data.table_descriptions.map((row, index) => <p key={index} className="whitespace-pre-wrap text-sm">{row.table_name}：{row.description}</p>)}
          </details>
          <button type="button" className="neo-button-secondary" onClick={kb.adoptPending}>采用服务器版本并丢弃本地修改</button>
        </div>}
        {kb.snapshot && <>
          <section className="space-y-3" aria-label="历史说明">
            <h3 className="text-lg font-bold">历史说明原文</h3>
            {!rows.length && <p className="text-sm text-gray-500">暂无历史说明。</p>}
            {rows.map((row, index) => {
              const key = 'h:' + index
              const effective = kb.effective.some(current => current.object_id && current.object_id === row.object_id && current.object_instance_version === row.object_instance_version)
              return <article key={index} className="neo-border p-4 rounded-lg space-y-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h4 className="font-bold">{row.table_name || '历史说明 ' + (index + 1)}</h4>
                  <span className="text-xs bg-gray-100 px-2 py-1 rounded">{provenance(row.provenance)}</span>
                  <span className="text-xs text-gray-600">{row.state === 'mapped' ? effective ? '当前有效映射' : '已映射，当前不可用' : '待映射'}</span>
                </div>
                <label className="block text-sm">说明
                  <textarea aria-label={'历史说明 ' + (index + 1)} className="neo-input w-full mt-1 min-h-20" disabled={kb.busy || !access.allowed}
                    value={kb.getText(key, row.description)} onChange={event => kb.changeText(key, row.description, event.target.value)} />
                </label>
                {row.field_descriptions.map(field => <label key={field.field_name} className="block text-sm">
                  <span>{field.field_name} · {provenance(field.provenance)}</span>
                  <textarea aria-label={'历史 ' + (index + 1) + ' 字段 ' + field.field_name} className="neo-input w-full mt-1" disabled={kb.busy || !access.allowed}
                    value={kb.getText(key, field.description, field.field_name)} onChange={event => kb.changeText(key, field.description, event.target.value, field.field_name)} />
                </label>)}
                {kb.v2 && <div className="flex flex-wrap items-center gap-3">
                  <label className="flex items-center gap-2">
                    <input type="checkbox" aria-label={'映射历史 ' + (index + 1)} checked={kb.mappings[index]?.checked ?? false}
                      disabled={disabled || kb.dirty || !!kb.snapshot?.targetError} onChange={event => kb.chooseMapping(index, { checked: event.target.checked })} />纳入批量映射
                  </label>
                  <select aria-label={'历史 ' + (index + 1) + ' 映射目标'} className="neo-input flex-1 min-w-0" value={kb.mappings[index]?.objectId ?? ''}
                    disabled={disabled || kb.dirty || !!kb.snapshot?.targetError} onChange={event => kb.chooseMapping(index, { objectId: event.target.value })}>
                    <option value="">请选择当前已选且可读的对象</option>
                    {objects.map(object => <option key={object.object_id} value={object.object_id}>{object.name} · {object.object_type === 'view' ? '视图' : '表'}</option>)}
                  </select>
                </div>}
              </article>
            })}
          </section>
          <section className="space-y-3" aria-label="当前有效对象">
            <h3 className="text-lg font-bold">当前已选可读对象</h3>
            {!objects.length && <p className="text-sm text-gray-500">没有可用映射目标。历史原文不会自动删除或映射。</p>}
            {objects.map(object => {
              const key = 'o:' + object.object_id
              const effective = kb.effective.find(row => row.object_id === object.object_id)
              const currentHistory = rows.filter(row => row.state === 'mapped' && row.object_id === object.object_id &&
                !!object.object_instance_version && row.object_instance_version === object.object_instance_version)
              const newColumns = object.columns.filter(column => !currentHistory.some(row => row.field_descriptions.some(field =>
                field.field_name === column.name && field.state === 'mapped' && (!field.column_type || field.column_type === column.type))))
              return <article key={object.object_id} className="neo-border p-4 rounded-lg space-y-2">
                <h4 className="font-bold break-all">{object.name} <span className="text-xs font-normal">{object.object_type === 'view' ? '视图' : '表'}</span></h4>
                {effective && (!!effective.description || effective.field_descriptions.length > 0) && <div className="space-y-2">
                  <p className="text-xs text-green-700">当前有效说明 · {provenance(effective.provenance)}</p>
                  <p className="whitespace-pre-wrap">{effective.description}</p>
                  {effective.field_descriptions.map(field => <p key={field.field_name} className="text-sm">{field.field_name}：{field.description}</p>)}
                </div>}
                {currentHistory.length > 0 && <p className="text-xs text-gray-500">修改已有原文请使用上方对应历史说明。</p>}
                {(!currentHistory.length || newColumns.length > 0) && <>
                  <p className="text-xs text-gray-500">可添加人工说明；历史内容不会自动继承。</p>
                  {!currentHistory.length && <textarea aria-label={'新对象说明 ' + object.name} className="neo-input w-full" disabled={disabled}
                    value={kb.getText(key, '')} onChange={event => kb.changeText(key, '', event.target.value)} />}
                  {newColumns.map(column => <label key={column.name} className="block text-sm">{column.name}
                    <textarea aria-label={'新对象 ' + object.name + ' 字段 ' + column.name} className="neo-input w-full mt-1" disabled={disabled}
                      value={kb.getText(key, '', column.name)} onChange={event => kb.changeText(key, '', event.target.value, column.name)} />
                  </label>)}
                </>}
              </article>
            })}
          </section>
        </>}
      </div>
      <div className="p-6 border-t-4 border-black space-y-3">
        {kb.dirty && <p className="text-sm text-amber-700">有未保存的说明。请先保存说明，再确认映射。</p>}
        {confirmClose ? <div className="space-y-2"><p>未保存的文字将丢失。</p><button className="neo-button-secondary mr-2" onClick={() => setConfirmClose(false)}>返回编辑</button><button className="neo-button" onClick={onClose}>放弃未保存修改并关闭</button></div> :
          <div className="flex flex-wrap gap-3">
            <button type="button" className="neo-button flex-1" onClick={() => void kb.save()} disabled={disabled || !kb.dirty}>保存说明</button>
            <button type="button" className="neo-button-secondary flex-1" onClick={() => void kb.confirm()} disabled={disabled || !kb.v2 || kb.dirty || !!kb.snapshot?.targetError}>确认批量映射</button>
            <button type="button" className="neo-button-secondary" onClick={close} disabled={kb.busy}>关闭</button>
          </div>}
      </div>
    </div>
  </div>
}
