import { ChevronDown, ChevronRight, Loader2, Search } from 'lucide-react'
import { objectKey, useObjectSelection } from '../../../hooks/useObjectSelection'
import type { TableInfo } from '../../../types/chat'
import { zhCN } from '../../../i18n/zh-CN'

function rowCountText(object: TableInfo) {
  if (object.row_count == null || (object.row_count_status && !['known', 'estimated'].includes(object.row_count_status))) return '行数未知'
  return object.row_count_status === 'estimated' ? '约 ' + object.row_count + ' 行（估算）' : object.row_count + ' 行'
}

interface SchemaTabProps { selection: ReturnType<typeof useObjectSelection> }
export default function SchemaTab({ selection: s }: SchemaTabProps) {
  const filtered = s.tables.filter(object => [object.name, object.schema_name, object.object_name].some(value => value?.toLowerCase().includes(s.search.toLowerCase())))
  const schemas = [...new Set(filtered.map(object => object.schema_name ?? ''))]
  return <div className="space-y-4">
    <div className="flex items-center justify-between gap-2">
      <h3 className="font-bold text-lg">选择查询对象</h3>
      <button type="button" onClick={() => void s.reload()} disabled={s.loading || s.saving} className="neo-button-secondary text-sm">重新加载对象</button>
    </div>
    {s.error && <p role="alert" className="p-4 bg-red-50 border-2 border-red-500 rounded-lg text-red-600">{s.error}</p>}
    {s.success && <p role="status" className="p-3 bg-green-50 border-2 border-green-500 rounded-lg text-green-700">{s.success}</p>}
    {s.loading ? <div className="flex items-center gap-2 py-12 justify-center"><Loader2 className="animate-spin w-6 h-6" />{zhCN.schema.loadingSchema}</div> :
      <fieldset disabled={s.blocked || s.saving} className="space-y-4">
        <div className="neo-border p-4 rounded-lg space-y-2">
          <label className="flex items-center gap-2 cursor-pointer">
            <input type="checkbox" className="w-5 h-5 rounded-md border-2 border-black"
              checked={s.allKinds?.length === 1 && s.allKinds[0] === 'table'} onChange={() => s.chooseAll(['table'])} />
            <span className="font-bold">{s.v2 ? '全部表（自动检测）' : zhCN.schema.allAuto}</span>
          </label>
          {s.v2 && <label className="flex items-center gap-2 cursor-pointer">
            <input type="checkbox" className="w-5 h-5 rounded-md border-2 border-black"
              checked={s.allKinds?.includes('table') === true && s.allKinds.includes('view')} onChange={() => s.chooseAll(['table', 'view'])} />
            <span className="font-bold">全部表和视图（自动检测）</span>
          </label>}
          <p className="text-gray-600 text-sm">默认只选择表；逐项勾选只保存当前对象，自动检测须主动选择。</p>
        </div>
        <div className="relative">
          <Search className="absolute left-3 top-3 w-4 h-4 text-gray-400" />
          <input aria-label="搜索查询对象" placeholder={zhCN.schema.search} className="neo-input pl-10 w-full"
            value={s.search} onChange={event => s.setSearch(event.target.value)} />
        </div>
        <p className="text-sm text-gray-600">{s.selected.length}/{s.tables.length} {zhCN.schema.selected}</p>
        <div className="space-y-4 max-h-[40vh] overflow-y-auto neo-border p-4 rounded-lg">
          {!filtered.length && <p className="text-gray-500">{s.search ? zhCN.schema.noMatch : '无可用结构；请检查对象选择与读取权限。'}</p>}
          {schemas.map(schema => <section key={schema} className="space-y-2">
            {schema && <h4 className="font-bold text-lg">{schema}</h4>}
            {filtered.filter(object => (object.schema_name ?? '') === schema).map(object => {
              const key = objectKey(object)
              return <div key={key} className="border-b border-gray-200 last:border-b-0 pb-2">
                <div className="flex items-center gap-2">
                  <button type="button" aria-label={(s.expanded[key] ? '收起 ' : '展开 ') + object.name}
                    onClick={() => s.toggleExpanded(key)} className="p-1 hover:bg-gray-100 rounded">
                    {s.expanded[key] ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
                  </button>
                  <label className="flex items-center gap-2 cursor-pointer flex-1 min-w-0">
                    <input type="checkbox" aria-label={object.name} className="w-4 h-4 rounded-md border-2 border-black"
                      checked={s.selected.includes(key)} onChange={() => s.toggle(key)} />
                    <span className="font-medium break-all">{object.name}</span>
                  </label>
                  {s.v2 && <span className="text-xs bg-gray-100 px-2 py-1 rounded">{object.object_type === 'view' ? '视图' : '表'}</span>}
                  {object.partial_columns && <span className="text-xs text-amber-700">部分列</span>}
                </div>
                {s.expanded[key] && <div className="ml-8 mt-2 pl-2 border-l-2 border-gray-200 space-y-2">
                  <p className="text-xs text-gray-500">{rowCountText(object)}</p>
                  {object.columns.map(column => <div key={column.name} className="text-sm">
                    <span className="font-medium">{column.name}</span>
                    <span className="text-xs bg-gray-100 px-1.5 py-0.5 rounded ml-2">{column.type}</span>
                    <span className="text-xs text-gray-500 ml-2">{column.nullable === null ? '可空性未知' : (column.nullable ?? column.is_nullable) ? zhCN.schema.nullable : '不可为空'}</span>
                    {column.comment && <p className="text-gray-500">{column.comment}</p>}
                  </div>)}
                </div>}
              </div>
            })}
          </section>)}
        </div>
        {s.v2 && <p role="status" className="text-gray-500 text-sm">{s.tables.length ? '结构可用，执行未验证' : '无可用结构'} · {s.status}</p>}
      </fieldset>}
  </div>
}
