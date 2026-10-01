import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// vitest 没开 globals，RTL 不会自动清理；显式注册，避免用例间 DOM 串扰。
afterEach(() => {
  cleanup()
})
