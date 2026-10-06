import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// vitest 没开 globals，RTL 不会自动清理；显式注册，避免用例间 DOM 串扰。
afterEach(() => {
  cleanup()
  // 导航状态镜像在 URL 上：用例之间要把地址栏复位，否则下一个 NavProvider
  // 会从上一个用例留下的路径还原出错误的 tab/栈。
  window.history.replaceState(null, '', '/')
})
