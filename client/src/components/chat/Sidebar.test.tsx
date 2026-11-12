import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import Sidebar from './Sidebar'
import { UserContext } from '../../contexts/UserContext'
import { zhCN } from '../../i18n/zh-CN'
import type { Chat } from '../../types/chat'

vi.mock('../../services/analyticsService', () => ({
  default: {
    trackSidebarToggled: vi.fn(),
    trackLogout: vi.fn(),
    trackConnectionEdited: vi.fn(),
    trackConnectionDeleted: vi.fn(),
    trackConnectionSelected: vi.fn(),
    trackConnectionDuplicated: vi.fn(),
  },
  trackViewGuideTutorial: vi.fn(),
}))

const makeConnection = (id: string, database: string): Chat => ({
  id,
  user_id: 'u1',
  connection: {
    type: 'postgresql',
    host: '127.0.0.1',
    port: '5432',
    username: 'tester',
    database,
    is_example_db: false,
  },
  settings: {
    auto_execute_query: false,
    share_data_with_ai: false,
    non_tech_mode: false,
    auto_generate_visualization: false,
  },
  created_at: '2026-11-01T00:00:00.000Z',
  updated_at: '2026-11-04T10:00:00.000Z',
})

const userContextValue = {
  user: { id: 'u1', username: 'tester', created_at: '2026-01-01T00:00:00.000Z' },
  setUser: vi.fn(),
}

const renderSidebar = (connections: Chat[], selectedConnection: Chat | undefined) => {
  const onSelectConnection = vi.fn()
  const onEditConnection = vi.fn()
  render(
    <UserContext.Provider value={userContextValue}>
      <Sidebar
        isExpanded
        onToggleExpand={vi.fn()}
        connections={connections}
        onSelectConnection={onSelectConnection}
        onAddConnection={vi.fn()}
        onLogout={vi.fn()}
        onEditConnection={onEditConnection}
        selectedConnection={selectedConnection}
        isLoadingConnections={false}
        eventSource={null}
      />
    </UserContext.Provider>,
  )
  return { onSelectConnection, onEditConnection }
}

// The connection row container is the nearest positioned ancestor of the row button.
const connectionRow = (connection: Chat): HTMLElement => {
  const row = screen.getByTitle(connection.connection.database).closest('div.relative')
  if (!row) throw new Error(`connection row not found for ${connection.id}`)
  return row as HTMLElement
}

const openConnectionSettings = (connection: Chat) => {
  fireEvent.click(within(connectionRow(connection)).getByTitle(zhCN.sidebar.connectionMenu))
  fireEvent.click(screen.getByText(zhCN.sidebar.chatSettings))
}

describe('sidebar connection settings menu target', () => {
  afterEach(() => {
    cleanup()
    vi.clearAllMocks()
  })

  test('binds settings to the menu Chat when names match and selection is stale', () => {
    const sharedName = '同名连接但身份不同的完整展示名称'
    const connA = { ...makeConnection('conn-a', 'db-a'), display_name: sharedName }
    const connB = { ...makeConnection('conn-b', 'db-b'), display_name: sharedName }
    const { onSelectConnection, onEditConnection } = renderSidebar([connA, connB], connA)

    // Mount-time selection of the currently selected connection must not leak into this scenario.
    onSelectConnection.mockClear()

    const secondRow = screen.getAllByTitle(sharedName)[1].closest('div.relative')
    if (!secondRow) throw new Error('second same-name Chat row is missing')
    fireEvent.click(within(secondRow as HTMLElement).getByTitle(zhCN.sidebar.connectionMenu))
    fireEvent.click(screen.getByText(zhCN.sidebar.chatSettings))

    // The modal-open callback must carry the menu target itself: App relies on this argument
    // to settle selectedConnection before opening the modal (display and PATCH target).
    expect(onEditConnection).toHaveBeenCalledTimes(1)
    const [editTarget] = onEditConnection.mock.calls[0]
    expect(editTarget).toBeTruthy()
    expect(editTarget.id).toBe('conn-b')
    expect(editTarget.connection.database).toBe('db-b')

    // The existing selection flow is still triggered in parallel for the menu target.
    expect(onSelectConnection).toHaveBeenCalledTimes(1)
    expect(onSelectConnection).toHaveBeenCalledWith('conn-b')
  })

  test('keeps binding to the menu connection when it is the currently selected one', () => {
    const connA = makeConnection('conn-a', 'db-a')
    const connB = makeConnection('conn-b', 'db-b')
    const { onSelectConnection, onEditConnection } = renderSidebar([connA, connB], connA)

    onSelectConnection.mockClear()

    openConnectionSettings(connA)

    expect(onEditConnection).toHaveBeenCalledTimes(1)
    const [editTarget] = onEditConnection.mock.calls[0]
    expect(editTarget).toBeTruthy()
    expect(editTarget.id).toBe('conn-a')
    expect(editTarget.connection.database).toBe('db-a')

    expect(onSelectConnection).toHaveBeenCalledTimes(1)
    expect(onSelectConnection).toHaveBeenCalledWith('conn-a')
  })

  test('requires the complete display name to authorize delete confirmation', () => {
    const fullName = 'yanfa-erp-这是超过侧栏宽度的完整名称'
    const connection = { ...makeConnection('conn-b', 'bi_agent'), display_name: `  ${fullName}  ` }
    renderSidebar([connection], undefined)

    const row = screen.getByTitle(fullName).closest('div.relative')
    if (!row) throw new Error('named Chat row is missing')
    fireEvent.click(within(row as HTMLElement).getByTitle(zhCN.sidebar.connectionMenu))
    fireEvent.click(screen.getByText(zhCN.sidebar.deleteChat))

    const input = screen.getByRole('textbox')
    const confirm = screen.getByRole('button', { name: zhCN.modal.deleteChat })
    fireEvent.change(input, { target: { value: 'bi_agent' } })
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: `${fullName.slice(0, 20)}...` } })
    expect(confirm).toBeDisabled()
    fireEvent.change(input, { target: { value: fullName } })
    expect(confirm).toBeEnabled()
  })
})
