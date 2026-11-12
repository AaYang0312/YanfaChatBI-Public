import { Workflow as WorkflowIcon, X } from 'lucide-react'
import { type KeyboardEvent, type ReactNode, useEffect, useId, useLayoutEffect, useRef } from 'react'
import { zhCN } from '../../i18n/zh-CN'
import type { Workflow } from '../../types/assistant'

interface WorkflowMenuProps {
  open: boolean
  workflow: Workflow | null
  onOpenChange: (open: boolean) => void
  onSelect: (workflow: Workflow) => void
  children: ReactNode
}

const copy = zhCN.assistant.menu
const workflows: { value: Workflow; label: string }[] = [
  { value: 'product_analysis', label: copy.productAnalysis },
  { value: 'sales_comparison', label: copy.salesComparison },
]

export default function WorkflowMenu({ open, workflow, onOpenChange, onSelect, children }: WorkflowMenuProps) {
  const id = useId()
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const wasOpen = useRef(false)
  const restoreFocus = useRef(true)
  const openingEdge = useRef<'first' | 'last' | null>(null)

  useLayoutEffect(() => {
    if (open) {
      restoreFocus.current = true
      const items = menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')
      if (openingEdge.current && items?.length) {
        items[openingEdge.current === 'last' ? items.length - 1 : 0].focus()
      } else if (workflow) {
        panel.current?.querySelector<HTMLElement>('textarea, input, select')?.focus()
      } else {
        items?.[0]?.focus()
      }
      openingEdge.current = null
    } else if (wasOpen.current && restoreFocus.current) {
      trigger.current?.focus()
    }
    wasOpen.current = open
  }, [open, workflow])

  useEffect(() => {
    if (!open) return
    const closeOutside = (event: Event) => {
      if (event.target instanceof Node && !root.current?.contains(event.target)) {
        restoreFocus.current = false
        onOpenChange(false)
      }
    }
    document.addEventListener('pointerdown', closeOutside)
    document.addEventListener('focusin', closeOutside)
    return () => {
      document.removeEventListener('pointerdown', closeOutside)
      document.removeEventListener('focusin', closeOutside)
    }
  }, [open, onOpenChange])

  const navigate = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')
    if (!items?.length || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const index = Array.from(items).indexOf(document.activeElement as HTMLButtonElement)
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1
      : (index + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length
    items[next].focus()
  }

  return (
    <div ref={root} className="shrink-0 self-end mb-1.5" onKeyDown={event => {
      if (event.key === 'Escape' && open) {
        event.preventDefault()
        event.stopPropagation()
        restoreFocus.current = true
        onOpenChange(false)
      }
    }}>
      <button
        ref={trigger}
        type="button"
        className="neo-button flex h-14 items-center gap-2 px-3"
        title={copy.title}
        aria-label={copy.title}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => { restoreFocus.current = true; onOpenChange(!open) }}
        onKeyDown={event => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault()
            openingEdge.current = event.key === 'ArrowUp' ? 'last' : 'first'
            if (open) {
              const items = menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')
              items?.[event.key === 'ArrowUp' ? items.length - 1 : 0]?.focus()
              openingEdge.current = null
            } else onOpenChange(true)
          }
        }}
      >
        <WorkflowIcon aria-hidden="true" className="h-5 w-5" />
        <span className="hidden md:inline">{copy.title}</span>
      </button>
      <div
        id={id}
        hidden={!open}
        role="dialog"
        aria-labelledby={`${id}-title`}
        className="absolute bottom-full left-0 right-0 z-20 mb-3 max-h-[min(65dvh,calc(100dvh-12rem))] overflow-y-auto overscroll-contain border-4 border-black bg-white shadow-[4px_4px_0px_rgba(0,0,0,1)]"
      >
        <div className="sticky top-0 z-10 flex items-center justify-between gap-2 border-b-2 border-black bg-[#FFDB58] px-3 py-2">
          <h2 id={`${id}-title`} className="font-semibold">{copy.title}</h2>
          <button type="button" className="neo-button-secondary p-2" aria-label={copy.close} onClick={() => {
            restoreFocus.current = true
            onOpenChange(false)
          }}><X aria-hidden="true" className="h-4 w-4" /></button>
        </div>
        <div ref={menu} role="menu" aria-label={copy.title} className="flex flex-wrap gap-2 p-3" onKeyDown={navigate}>
          {workflows.map(item => (
            <button
              key={item.value}
              type="button"
              role="menuitemradio"
              aria-checked={workflow === item.value}
              className={workflow === item.value ? 'neo-button' : 'neo-button-secondary'}
              onClick={() => onSelect(item.value)}
            >{item.label}</button>
          ))}
        </div>
        <div ref={panel} hidden={!workflow}>{children}</div>
      </div>
    </div>
  )
}
