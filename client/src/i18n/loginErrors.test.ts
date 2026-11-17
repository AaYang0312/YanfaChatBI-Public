import { describe, expect, test } from 'vitest'
import { formatServiceError, UNKNOWN_ERROR_SUMMARY } from './zh-CN'

const credentialsError = '\u8d26\u53f7\u6216\u5bc6\u7801\u9519\u8bef\uff0c\u8bf7\u91cd\u8bd5\u3002'

describe('login error summaries', () => {
  test.each([
    'invalid password',
    'Invalid credentials, User does not exist.',
    'Invalid credentials. Please try again.',
  ])('uses an account-neutral message for %s', (message) => {
    expect(formatServiceError(new Error(message))).toEqual({ summary: credentialsError })
    expect(formatServiceError({ response: { status: 401, data: { error: message } } }).summary).toBe(credentialsError)
  })

  test('does not turn service outages into credential errors', () => {
    expect(formatServiceError(new Error('upstream unavailable')).summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(formatServiceError(null, 503).summary).toBe(UNKNOWN_ERROR_SUMMARY)
  })
})
