// 调用详情页：元信息（时延/token/开销）与请求/响应载荷块。
import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderPage } from "../test/renderPage";
import { startTestServer } from "../test/server";
import { AILogItemPage } from "./AILogItemPage";

startTestServer();

describe("AILogItemPage", () => {
  it("渲染元信息与请求/响应全文", async () => {
    renderPage(<AILogItemPage id={42} />);

    const meta = await screen.findByTestId("ailogitem-meta");
    expect(meta).toHaveTextContent("812ms");
    expect(meta).toHaveTextContent("1451+0");
    expect(meta).toHaveTextContent("端点 systemone");

    const input = await screen.findByTestId("ailogitem-input");
    // 固定提示词已剥成占位符；动态正文原样保留。
    expect(input).toHaveTextContent("固定提示词已省略");
    expect(input).toHaveTextContent("（mock）正文");

    const output = await screen.findByTestId("ailogitem-output");
    expect(output).toHaveTextContent('"choice"');
  });
});
