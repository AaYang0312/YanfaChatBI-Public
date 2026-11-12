import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import LoginForm from '../auth/LoginForm'
import ChartRenderer from '../ChartRenderer'
import { productName, zhCN } from '../../i18n/zh-CN'
import type { ChartConfiguration } from '../../types/visualization'

vi.mock('../../services/analyticsService', () => ({
  default: { trackEvent: vi.fn() },
}))

vi.mock('../../utils/googleOAuth', () => ({
  initiateGoogleOAuth: vi.fn(),
}))

const CLIENT_ROOT = path.resolve(process.cwd())
const REPO_ROOT = path.resolve(CLIENT_ROOT, '..')

const SCAN_TARGETS = [
  path.join(CLIENT_ROOT, 'src'),
  path.join(CLIENT_ROOT, 'index.html'),
]

const TEXT_EXT = new Set(['.ts', '.tsx', '.js', '.jsx', '.html', '.css', '.md', '.json'])

function walk(target: string): string[] {
  const st = statSync(target)
  if (st.isFile()) return [target]
  const out: string[] = []
  for (const entry of readdirSync(target)) {
    if (entry === 'node_modules' || entry === 'dist' || entry === 'coverage') continue
    out.push(...walk(path.join(target, entry)))
  }
  return out
}

function coveredBy(line: string, start: number, end: number, pattern: RegExp): boolean {
  const flags = pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`
  const expression = new RegExp(pattern.source, flags)
  let found: RegExpExecArray | null
  while ((found = expression.exec(line)) !== null) {
    const foundStart = found.index
    const foundEnd = foundStart + found[0].length
    if (foundStart <= start && foundEnd >= end) return true
    if (expression.lastIndex === found.index) expression.lastIndex += 1
  }
  return false
}

export function isExempt(source: string, index: number, match: string): boolean {
  const lineStart = source.lastIndexOf('\n', index) + 1
  const lineEnd = source.indexOf('\n', index)
  const line = source.slice(lineStart, lineEnd === -1 ? source.length : lineEnd)
  const column = index - lineStart
  const tokenEnd = column + match.length
  if (coveredBy(line, column, tokenEnd, /https?:\/\/[^\s'"<>]+/i)) return true
  if (coveredBy(line, column, tokenEnd, /mailto:[^\s'"<>]+/i)) return true
  if (coveredBy(line, column, tokenEnd, /\bVITE_YANFA_[A-Z0-9_]+\b/i)) return true
  if (coveredBy(line, column, tokenEnd, /\byanfachatbi[-_](?:logo|banner)\.[A-Za-z0-9]+\b/i)) return true
  if (coveredBy(line, column, tokenEnd, /\byanfachatbi_recommendation_tooltip_shown\b/)) return true
  if (coveredBy(line, column, tokenEnd, /YanfaChatBI is analyzing your request\.\./) && /response\.data\s*===/.test(line)) return true
  return false
}


describe('brand URL exemption', () => {
  test('does not exempt visible brand text beside a URL', () => {
    const source = '<a href="https://example.invalid">YanfaChatBI</a>'
    const url = source.indexOf('example.invalid')
    const label = source.indexOf('YanfaChatBI')
    expect(isExempt(source, url, 'example')).toBe(true)
    expect(isExempt(source, label, 'YanfaChatBI')).toBe(false)
  })
  test('does not exempt YanfaChatBI link text beside GitHub or mailto', () => {
    const github = '<a href="https://github.com/example/project">YanfaChatBI</a>'
    const mail = '<a href="mailto:help@example.com">YanfaChatBI</a>'
    expect(isExempt(github, github.indexOf('example/project'), 'example')).toBe(true)
    expect(isExempt(github, github.indexOf('YanfaChatBI'), 'YanfaChatBI')).toBe(false)
    expect(isExempt(mail, mail.indexOf('help@example.com'), 'example')).toBe(true)
    expect(isExempt(mail, mail.indexOf('YanfaChatBI'), 'YanfaChatBI')).toBe(false)
  })
  test('exempts only the identifier token and keeps plus-addressed mail', () => {
    const logo = '<a href="https://example.invalid">YanfaChatBI-logo</a>'
    const invite = '<a href="mailto:help@example.com">Invite_YanfaChatBI_users</a>'
    const plus = 'mailto:help@example.com'
    expect(isExempt(logo, logo.indexOf('YanfaChatBI'), 'YanfaChatBI')).toBe(false)
    expect(isExempt(invite, invite.indexOf('YanfaChatBI'), 'YanfaChatBI')).toBe(false)
    expect(isExempt(plus, plus.indexOf('help@example.com'), 'example')).toBe(true)
  })


})



const EMPTY_CHART: ChartConfiguration = {
  chart_type: 'bar',
  title: 'revenue',
  data_fetch: {
    query_strategy: 'original_query',
    optimized_query: 'select 1',
    projected_rows: 0,
  },
  chart_render: {
    type: 'bar',
    x_axis: { data_key: 'x', label: 'x', type: 'category' },
    colors: ['#FFD700'],
    features: { tooltip: false, legend: false, grid: false, responsive: true, zoom_enabled: false },
  },
  rendering_hints: {
    chart_height: 400,
    color_scheme: 'primary',
    should_aggregate_beyond: 1000,
    projected_row_count: 0,
    data_density: 'sparse',
  },
}

describe('brand coverage', () => {
  test('fails when product-brand YanfaChatBI remains in client or landing display sources', () => {
    const violations: string[] = []
    for (const target of SCAN_TARGETS) {
      for (const file of walk(target)) {
        if (file.endsWith('.test.ts') || file.endsWith('.test.tsx')) continue
        const ext = path.extname(file)
        if (ext && !TEXT_EXT.has(ext)) continue
        const source = readFileSync(file, 'utf8')
        const re = /YanfaChatBI/gi
        let match: RegExpExecArray | null
        while ((match = re.exec(source)) !== null) {
          if (isExempt(source, match.index, match[0])) continue
          const line = source.slice(0, match.index).split('\n').length
          violations.push(`${path.relative(REPO_ROOT, file)}:${line} ${match[0]}`)
        }
      }
    }
    expect(violations, violations.join('\n')).toEqual([])
  })
})

describe('login validation', () => {
  test('renders Chinese required-field validation on blur', () => {
    render(
      <LoginForm
        onLogin={vi.fn()}
        onSwitchToSignup={vi.fn()}
        onSwitchToForgotPassword={vi.fn()}
      />,
    )
    expect(screen.getByText(productName)).toBeInTheDocument()
    fireEvent.blur(screen.getByPlaceholderText(zhCN.auth.login.usernamePlaceholder))
    fireEvent.blur(screen.getByPlaceholderText(zhCN.auth.login.passwordPlaceholder))
    expect(screen.getByText(zhCN.auth.login.usernameRequired)).toBeInTheDocument()
    expect(screen.getByText(zhCN.auth.login.passwordRequired)).toBeInTheDocument()
  })

  test('shows the startup authentication failure in Chinese', () => {
    render(
      <LoginForm
        onLogin={vi.fn()}
        onSwitchToSignup={vi.fn()}
        onSwitchToForgotPassword={vi.fn()}
        startupError="服务暂时不可用，请稍后重试。"
      />,
    )
    expect(screen.getByText('服务暂时不可用，请稍后重试。')).toBeInTheDocument()
  })

})

describe('chart empty state', () => {
  test('renders Chinese empty copy without English fallback', () => {
    render(<ChartRenderer config={EMPTY_CHART} data={[]} />)
    expect(screen.getByText(zhCN.chart.empty)).toBeInTheDocument()
    expect(screen.queryByText(/No data available for visualization/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/An Error Occurred/i)).not.toBeInTheDocument()
  })
})
