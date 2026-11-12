import { expect, test } from 'vitest'
import { render, screen } from '@testing-library/react'

test('runner smoke renders runner-ok', () => {
  render(<p>runner-ok</p>)
  expect(screen.getByText('runner-ok')).toBeInTheDocument()
})
