import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

describe("Modal", () => {
  it("没有说明文案时不引用不存在的描述节点", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    render(<Modal open title="测试弹窗" onClose={() => undefined}><p>内容</p></Modal>);

    expect(screen.getByRole("dialog", { name: "测试弹窗" })).not.toHaveAttribute("aria-describedby");
    expect(consoleError).not.toHaveBeenCalled();
  });
});
