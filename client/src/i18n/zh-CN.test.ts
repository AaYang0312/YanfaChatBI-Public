import { describe, expect, test } from 'vitest'
import { transformBackendMessage } from '../types/messages'
import type { BackendMessage } from '../types/messages'

import {
  extendCopy,
  formatServiceError,
  productName,
  TECHNICAL_DETAILS_LABEL,
  translateSseStatus,
  UNKNOWN_ERROR_SUMMARY,
  zhCN,
} from './zh-CN'
function backendMessage(content: string): BackendMessage {
  return {
    id: 'message-1',
    chat_id: 'chat-1',
    type: 'assistant',
    content,
    is_edited: false,
    created_at: '2026-11-02T00:00:00Z',
  }
}

describe('historical assistant messages', () => {
  test('keeps ordinary answers unchanged', () => {
    const answer = '请在 PostgreSQL 中执行以下查询：SELECT * FROM orders'
    const message = transformBackendMessage(backendMessage(answer.repeat(20)))
    expect(message.content).toBe(answer.repeat(20))
    expect(message.serviceError).toBeUndefined()

    const explained = 'Error: relation "orders" does not exist.\n\n这是数据库错误示例，下面是解释与解决方案。' + '补充说明。'.repeat(180)
    const explanation = transformBackendMessage(backendMessage(explained))
    expect(explanation.content).toBe(explained)
    expect(explanation.serviceError).toBeUndefined()
  })

  test('maps reloaded cancellation and error prefixes without rendering raw text', () => {
    const cancelled = transformBackendMessage(backendMessage('Operation cancelled by user'))
    expect(cancelled.content).toBe('已取消。')
    expect(cancelled.serviceError).toMatchObject({ summary: '已取消。', detail: 'Operation cancelled by user' })


    const failed = transformBackendMessage(backendMessage('Error: token=DEMO_SECRET_VALUE'))
    expect(failed.content).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(failed.content).not.toMatch(/DEMO_SECRET_VALUE/)
    expect(failed.serviceError).toMatchObject({ summary: UNKNOWN_ERROR_SUMMARY })
    expect(formatServiceError(failed.serviceError).summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(formatServiceError(failed.serviceError).detail).not.toMatch(/DEMO_SECRET_VALUE/)

    const cancelledAgain = formatServiceError(cancelled.serviceError)
    expect(cancelledAgain.summary).toBe('已取消。')
    expect(cancelledAgain.summary).not.toBe(UNKNOWN_ERROR_SUMMARY)
  })


})



describe('zhCN catalog', () => {
  test('exports frozen catalog and product name 元发问数平台', () => {
    expect(productName).toBe('元发问数平台')
    expect(zhCN.productName).toBe('元发问数平台')
    expect(Object.isFrozen(zhCN)).toBe(true)
    expect(Object.isFrozen(zhCN.auth.login)).toBe(true)
  })

  test('extendCopy merges extra keys without unfreezing the catalog', () => {
    const extra = { taskExtraKey: '补充文案' }
    const merged = extendCopy(extra)
    expect(merged.taskExtraKey).toBe('补充文案')
    expect(merged.productName).toBe('元发问数平台')
    expect(Object.isFrozen(zhCN)).toBe(true)
    expect('taskExtraKey' in zhCN).toBe(false)
  })
})

describe('formatServiceError', () => {
  test('maps known error codes and HTTP statuses to Chinese', () => {
    expect(formatServiceError({ code: 'UNAUTHORIZED' }).summary).toBe(zhCN.errors.codes.UNAUTHORIZED)
    expect(formatServiceError({ code: '401' }).summary).toBe(zhCN.errors.http[401])
    expect(formatServiceError(null, 403).summary).toBe(zhCN.errors.http[403])
    expect(formatServiceError({ code: 'CONNECTION_ERROR' }).summary).toBe(zhCN.errors.codes.CONNECTION_ERROR)
    expect(formatServiceError({ code: 'QUERY_EXECUTION_FAILED' }).summary).toBe(
      zhCN.errors.codes.QUERY_EXECUTION_FAILED,
    )
  })

  test('unknown English errors use the safe summary and keep original under 技术详情', () => {
    const original = 'socket hang up while contacting upstream'
    const result = formatServiceError(original)
    expect(result.summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(result.detail).toBe(original)
    expect(TECHNICAL_DETAILS_LABEL).toBe('技术详情')
  })

  test('does not put secrets into technical details', () => {
    const result = formatServiceError('token=sk-live-secret password=hunter2 failed')
    expect(result.summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(result.detail).toBeDefined()
    expect(result.detail).not.toMatch(/sk-live-secret/)
    expect(result.detail).not.toMatch(/hunter2/)
  })

  test('redacts quoted JSON secrets and drops report payloads from technical details', () => {
    const secret = formatServiceError('{"api_key":"DEMO_SECRET_VALUE","error":"connection failed"}')
    expect(secret.summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(secret.detail).toBeDefined()
    expect(secret.detail).not.toMatch(/DEMO_SECRET_VALUE/)
    expect(secret.detail).toMatch(/connection failed/)

    const report = formatServiceError('report payload: {"revenue":123456,"customer_id":"EXAMPLE123"}')
    expect(report.summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(report.detail ?? '').not.toMatch(/123456/)
    expect(report.detail ?? '').not.toMatch(/EXAMPLE123/)
    expect(report.detail ?? '').toMatch(/"revenue":"\[redacted\]"/)
    expect(report.detail ?? '').toMatch(/"customer_id":"\[redacted\]"/)

  })
  test('redacts compound secret names, report variants, and SQL password literals', () => {
    const secret = formatServiceError('{"access_token":"DEMO_ACCESS","db_password":"DEMO_DB_PASSWORD","error":"connection failed"}')
    expect(secret.detail).toMatch(/connection failed/)
    expect(secret.detail).not.toMatch(/DEMO_ACCESS/)
    expect(secret.detail).not.toMatch(/DEMO_DB_PASSWORD/)

    const report = formatServiceError('report payload: {"region":"杭州","net_income":234567,"account_name":"DEMO_ACCOUNT"}')
    expect(report.detail ?? '').not.toMatch(/杭州/)
    expect(report.detail ?? '').not.toMatch(/234567/)
    expect(report.detail ?? '').not.toMatch(/DEMO_ACCOUNT/)

    const sql = formatServiceError({
      message: 'query failed',
      details: "SELECT 'DEMO_SECRET_VALUE' AS password FROM missing_table LIMIT 1",
    })
    expect(sql.detail).toMatch(/missing_table/)
    expect(sql.detail).not.toMatch(/DEMO_SECRET_VALUE/)
  })
  test('redacts nested objects, quoted password aliases, and insert values', () => {
    const nested = formatServiceError('report payload: {"meta":{"page":1},"region":"杭州","net_income":234567,"account_name":"DEMO_ACCOUNT"}')
    expect(nested.detail).toMatch(/"page":1/)
    expect(nested.detail).not.toMatch(/杭州/)
    expect(nested.detail).not.toMatch(/234567/)
    expect(nested.detail).not.toMatch(/DEMO_ACCOUNT/)

    const quoted = formatServiceError('SELECT \'DEMO_QUOTED_SECRET\' AS "password" FROM missing_table')
    expect(quoted.detail).toMatch(/missing_table/)
    expect(quoted.detail).not.toMatch(/DEMO_QUOTED_SECRET/)

    const inserted = formatServiceError("INSERT INTO users (password) VALUES ('DEMO_INSERT_SECRET')")
    expect(inserted.detail).toMatch(/INSERT INTO users/)
    expect(inserted.detail).not.toMatch(/DEMO_INSERT_SECRET/)
  })


  test('short English mixed with Chinese uses the safe summary', () => {
    expect(formatServiceError('DB 错误：用户查询失败').summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(formatServiceError('SQL 错误：无法访问用户表').summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(formatServiceError('db错误：用户查询失败').summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(formatServiceError('sql错误：无法访问用户表').summary).toBe(UNKNOWN_ERROR_SUMMARY)
  })

  test('prefers the backend error over the Axios request wrapper', () => {
    const result = formatServiceError({
      message: 'Request failed with status code 400',
      response: { status: 400, data: { error: 'query does not support pagination' } },
    }, 400)
    expect(result.summary).toBe(zhCN.errors.http[400])
    expect(result.detail).toMatch(/query does not support pagination/)
    expect(result.detail).not.toMatch(/Request failed with status code 400/)
  })

  test('keeps a safe reason for dashboard generation failure', () => {
    const failed = translateSseStatus("Failed to generate dashboard 'Sales': rate limit exceeded")
    expect(failed).toContain('生成仪表盘失败。')
    expect(failed).toContain('rate limit exceeded')
    expect(failed).not.toMatch(/DEMO_SECRET_VALUE/)
  })

  test('does not translate an already translated tool status again', () => {
    const once = translateSseStatus('Exploring database: calling get_table_info')
    expect(once).toBe('正在查看数据库：get_table_info')
    expect(translateSseStatus(once)).toBe(once)
    expect(once).not.toBe(UNKNOWN_ERROR_SUMMARY)

    const leaked = translateSseStatus('Exploring: 用户查询 token=DEMO_SSE_SECRET')
    expect(leaked).toContain('正在查看')
    expect(leaked).not.toMatch(/DEMO_SSE_SECRET/)

    const building = translateSseStatus('Building dashboard 1/2: 销售分析...')
    expect(building).toBe('正在构建仪表盘 1/2：销售分析')
    expect(building).not.toBe(UNKNOWN_ERROR_SUMMARY)
  })



  test('mixed Chinese and English errors still use the safe summary and redaction', () => {
    expect(formatServiceError('Database error: 用户查询失败').summary).toBe(UNKNOWN_ERROR_SUMMARY)
    const leaked = formatServiceError('连接失败 password=DEMO_SECRET_VALUE')
    expect(leaked.summary).toBe(UNKNOWN_ERROR_SUMMARY)
    expect(leaked.summary).not.toMatch(/DEMO_SECRET_VALUE/)
    expect(leaked.detail).not.toMatch(/DEMO_SECRET_VALUE/)
  })

  test('keeps safety reasons and the error code without replacing them with unavailable', () => {
    const blocked = formatServiceError({
      code: 'SAFETY_VIOLATION',
      message: 'Query blocked by safety validation',
      details: 'SAFETY VIOLATION: DELETE without WHERE clause would delete ALL records. Please add a WHERE condition to specify which records to delete',
    })
    expect(blocked.summary).toBe(zhCN.errors.codes.SAFETY_VIOLATION)
    expect(blocked.summary).not.toBe(UNKNOWN_ERROR_SUMMARY)
    expect(blocked.code).toBe('SAFETY_VIOLATION')
    expect(blocked.detail).toMatch(/Please add a WHERE condition/)
    expect(blocked.detail).toMatch(/SAFETY VIOLATION/)

  })

  test('maps known business failures instead of service unavailable', () => {
    expect(formatServiceError({
      code: 'DUPLICATE_KEY',
      message: 'Document with the same unique key already exists',
    }).summary).toBe(zhCN.errors.codes.DUPLICATE_KEY)
    expect(formatServiceError('User with this email already exists').summary).toBe('该邮箱已注册。')
    expect(formatServiceError('Invalid or expired OTP').summary).toBe('验证码无效或已过期。')
    const noRows = formatServiceError('No data rows returned by the query. Visualization requires at least one data row.')
    expect(noRows.summary).toBe(zhCN.viz.noData)
    expect(noRows.summary).not.toBe(UNKNOWN_ERROR_SUMMARY)
  })

  test('translates cancel, dashboard, and tool progress into Chinese summaries', () => {
    expect(translateSseStatus('Operation cancelled by user')).toBe('已取消。')
    expect(translateSseStatus('Analyzing your database schema...')).toBe('正在分析数据库结构…')
    expect(translateSseStatus('Preparing your response...')).toBe('正在准备回复…')
    const failed = translateSseStatus('Failed to generate dashboard demo: token=DEMO_SECRET_VALUE')
    expect(failed).toBe('生成仪表盘失败。')
    expect(failed).not.toMatch(/DEMO_SECRET_VALUE/)
    expect(failed).not.toMatch(/token=/)
  })

})
