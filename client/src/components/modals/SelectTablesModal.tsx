import { Database, X } from 'lucide-react'
import type { Chat } from '../../types/chat'
import { useSourceAccess } from '../../hooks/useSourceAccess'
import { useObjectSelection } from '../../hooks/useObjectSelection'
import SchemaTab from './components/SchemaTab'
import { zhCN } from '../../i18n/zh-CN'

interface SelectTablesModalProps {
  chat: Chat
  onClose: (updatedChat?: Chat) => void
  onSave: (selectedTables: string) => Promise<void>
}
export default function SelectTablesModal({ chat, onClose, onSave }: SelectTablesModalProps) {
  const access = useSourceAccess(chat.id)
  const selection = useObjectSelection(chat)
  const save = async () => {
    const updated = await selection.save(async (_id, value) => onSave(value))
    if (updated) onClose(updated)
  }
  return <div className="fixed inset-0 bg-black/50 backdrop-blur-sm flex items-center justify-center p-4 z-[200]">
    <div role="dialog" aria-label="选择查询对象" className="bg-white neo-border rounded-lg w-full max-w-2xl max-h-[90vh] overflow-hidden flex flex-col relative z-[201]">
      <div className="flex justify-between items-center p-6 border-b-4 border-black">
        <div className="flex items-center gap-2"><Database className="w-6 h-6" /><h2 className="text-2xl font-bold">选择查询对象</h2></div>
        <button type="button" aria-label={zhCN.common.close} disabled={selection.saving} onClick={() => onClose()} className="hover:bg-gray-100 rounded-lg p-2"><X className="w-6 h-6" /></button>
      </div>
      <div className="p-6 overflow-y-auto flex-grow"><SchemaTab selection={selection} /></div>
      <div className="p-6 border-t-4 border-black space-y-2">
        {!access.allowed && <p role="status">{access.reason}</p>}
        <div className="flex gap-4">
          <button type="button" onClick={() => void save()} disabled={selection.loading || selection.saving || selection.blocked || !access.allowed} className="neo-button flex-1">{selection.saving ? zhCN.schema.saving : zhCN.schema.saveApply}</button>
          <button type="button" onClick={() => onClose()} disabled={selection.saving} className="neo-button-secondary flex-1">{zhCN.common.cancel}</button>
        </div>
      </div>
    </div>
  </div>
}
