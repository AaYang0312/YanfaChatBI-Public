import { useState } from 'react'
import {
  formatServiceError,
  TECHNICAL_DETAILS_LABEL,
} from './zh-CN'

interface ServiceErrorViewProps {
  error?: unknown
  httpStatus?: number
  className?: string
}

export default function ServiceErrorView({
  error,
  httpStatus,
  className,
}: ServiceErrorViewProps) {
  const { summary, detail, code } = formatServiceError(error, httpStatus)
  const technical = [code ? `code: ${code}` : '', detail ?? ''].filter(Boolean).join('\n')
  const [open, setOpen] = useState(false)

  return (
    <div className={className}>
      <p className="font-medium">{summary}</p>
      {technical ? (
        <div className="mt-2">
          <button
            type="button"
            className="text-sm underline"
            onClick={() => setOpen(value => !value)}
          >
            {TECHNICAL_DETAILS_LABEL}
          </button>
          {open ? (
            <pre className="mt-2 text-xs whitespace-pre-wrap break-all opacity-80">{technical}</pre>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
