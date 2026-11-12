import { describe, expect, test } from 'vitest'
import { transformBackendMessage, type BackendMessage } from './messages'

function oldMessage(): BackendMessage {
  return {
    id: 'msg-old',
    chat_id: 'chat-1',
    type: 'assistant',
    content: 'SELECT 1',
    is_edited: false,
    created_at: '2026-09-01T00:00:00+08:00',
    queries: [],
  }
}

describe('assistant message reference', () => {
  test('keeps an old message without assistant_run_id', () => {
    const message = transformBackendMessage(oldMessage())
    expect(message.id).toBe('msg-old')
    expect(message.content).toBe('SELECT 1')
    expect(message.assistant_run_id).toBeUndefined()
    expect(message.queries).toEqual([])
  })

  test('preserves assistant_run_id through the DTO transform', () => {
    const message = transformBackendMessage({ ...oldMessage(), assistant_run_id: 'run-numeric' })
    expect(message.assistant_run_id).toBe('run-numeric')
  })
})
