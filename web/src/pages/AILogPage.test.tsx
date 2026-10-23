// 模型调用流水页：结果徽标与用量行、失败行带阶段与错误、
// 状态 chips 发服务端筛选、非主管理员的权限空态（且不发请求）。
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { HttpResponse, http } from "msw";
import { describe, expect, it } from "vitest";
import type { AILogsResp, State } from "../api/types";
import { mockState } from "../mocks/fixtures";
import { renderPage } from "../test/renderPage";
import { server, startTestServer } from "../test/server";
import { AILogPage } from "./AILogPage";

startTestServer();

const EMPTY: AILogsResp = { logs: [], page: 1, total: 0 };

describe("AILogPage", () => {
  it("渲染成功行与失败行的徽标、模型、用量", async () => {
    renderPage(<AILogPage />);

    const okRow = await screen.findByTestId("ailog-row-42");
    expect(okRow).toHaveTextContent("demo/gpt-5-mini");
    expect(okRow.querySelector('[data-tone="ok"]')).toHaveTextContent("成功");
    expect(okRow).toHaveTextContent("1451+0 tok");
    expect(okRow).toHaveTextContent("812ms");

    const errRow = screen.getByTestId("ailog-row-41");
    expect(errRow.querySelector('[data-tone="no"]')).toHaveTextContent("失败");
    expect(errRow).toHaveTextContent("首字超时");
    // 失败行把错误原因一并带出来，不用点进详情就能看到卡在哪个阶段。
    expect(errRow).toHaveTextContent("上游 demo 超过 15s 没有响应");
  });

  it("状态 chips 把筛选发到服务端", async () => {
    const bodies: Record<string, unknown>[] = [];
    server.use(
      http.post("*/miniapp/api/ailog", async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json(EMPTY);
      }),
    );
    renderPage(<AILogPage />);

    fireEvent.click(await screen.findByTestId("ailog-status-err"));
    fireEvent.click(screen.getByTestId("ailog-status-ok"));
    await waitFor(() => expect(bodies.length).toBeGreaterThanOrEqual(2));
    expect(bodies.at(-1)?.status).toBe("ok");
    expect(bodies.at(-2)?.status).toBe("err");
  });

  it("非主管理员显示权限空态且不发请求", async () => {
    const notMain: State = {
      ...mockState,
      me: { ...mockState.me, main: false },
    };
    const bodies: unknown[] = [];
    server.use(
      http.post("*/miniapp/api/ailog", async ({ request }) => {
        bodies.push(await request.json());
        return HttpResponse.json(EMPTY);
      }),
    );
    // renderPage 的 state 注入口与 SysLogPage.test 相同（mockState 覆盖）。
    server.use(
      http.post("*/miniapp/api/state", () => HttpResponse.json({ ...notMain })),
    );
    renderPage(<AILogPage />);

    expect(await screen.findByText("需要主管理员权限")).toBeInTheDocument();
    await waitFor(() => expect(bodies.length).toBe(0));
  });
});
