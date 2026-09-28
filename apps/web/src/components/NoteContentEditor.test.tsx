import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import "../styles.css";
import { NoteContentEditor } from "./NoteContentEditor";

function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <><NoteContentEditor value={value} onChange={setValue} /><output data-testid="markdown-value">{value}</output></>;
}

function ReplacementHarness() {
  const [value, setValue] = useState("原文");
  return <><button type="button" onClick={() => setValue("## 父级更新")}>替换草稿</button><NoteContentEditor value={value} onChange={setValue} /></>;
}

describe("NoteContentEditor", () => {
  it("在 Markdown 源码与预览之间同步唯一草稿", async () => {
    const user = userEvent.setup();
    render(<Harness initial="# 原标题" />);

    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });
    expect(source).toHaveValue("# 原标题");
    await user.clear(source);
    await user.type(source, "## 新标题{enter}{enter}- 第一项");
    await user.click(screen.getByRole("tab", { name: "预览" }));

    const preview = screen.getByRole("tabpanel", { name: "预览" });
    expect(within(preview).getByRole("heading", { level: 2, name: "新标题" })).toBeInTheDocument();
    expect(within(preview).getByRole("listitem")).toHaveTextContent("第一项");
    expect(screen.getByTestId("markdown-value")).toHaveTextContent("## 新标题");
  });

  it("预览支持 GFM 且不执行原始 HTML", async () => {
    const user = userEvent.setup();
    const markdown = "| 项目 | 状态 |\n| --- | --- |\n| 编辑器 | 完成 |\n\n- [x] 预览\n\n<script>window.hacked = true</script>\n<div onclick=\"window.hacked=true\">危险</div>";
    render(<Harness initial={markdown} />);
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.click(screen.getByRole("tab", { name: "预览" }));

    const preview = screen.getByRole("tabpanel", { name: "预览" });
    expect(within(preview).getByRole("table")).toBeInTheDocument();
    expect(within(preview).getByRole("checkbox")).toBeChecked();
    expect(preview.querySelector("script")).toBeNull();
    expect(preview.querySelector("div[onclick]")).toBeNull();
  });

  it("预览中的外部链接使用安全的新窗口属性", async () => {
    const user = userEvent.setup();
    render(<Harness initial="[外部](https://example.com) [内部](/notes)" />);
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    await user.click(screen.getByRole("tab", { name: "预览" }));

    expect(screen.getByRole("link", { name: "外部" })).toHaveAttribute("target", "_blank");
    expect(screen.getByRole("link", { name: "外部" })).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(screen.getByRole("link", { name: "内部" })).not.toHaveAttribute("target");
  });

  it("默认以富文本显示已有 Markdown 和约定工具栏", () => {
    render(<Harness initial={"# 已有标题\n\n正文"} />);

    expect(screen.getByRole("tab", { name: "富文本" })).toHaveAttribute("aria-selected", "true");
    const editor = screen.getByRole("region", { name: "富文本正文" });
    expect(within(editor).getByRole("heading", { level: 1, name: "已有标题" })).toBeInTheDocument();
    expect(within(editor).getByRole("textbox", { name: "富文本正文" })).toBeInTheDocument();
    expect(within(editor).getByRole("toolbar", { name: "正文格式" })).toBeInTheDocument();
    expect(within(editor).getByRole("radio", { name: "粗体" })).toBeInTheDocument();
    expect(within(editor).getByRole("button", { name: "插入链接" })).toBeInTheDocument();
  });

  it("段落格式菜单使用中文并只提供约定块类型", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.click(screen.getByRole("combobox", { name: "段落格式" }));

    expect(await screen.findByRole("option", { name: "正文" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "引用" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "一级标题" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "二级标题" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "三级标题" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "四级标题" })).toBeNull();
  });

  it("编辑表面保留桌面高度并让长内容在局部滚动", async () => {
    const user = userEvent.setup();
    render(<Harness initial={"```ts\nconst value = 1;\n```"} />);

    const richPanel = screen.getByRole("tabpanel", { name: "富文本" });
    expect(getComputedStyle(richPanel).minHeight).toBe("320px");
    expect(getComputedStyle(screen.getByRole("toolbar", { name: "正文格式" })).overflowX).toBe("auto");

    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });
    expect(getComputedStyle(source).minHeight).toBe("320px");
    expect(getComputedStyle(source).resize).toBe("none");

    await user.click(screen.getByRole("tab", { name: "预览" }));
    const codeBlock = within(screen.getByRole("tabpanel", { name: "预览" })).getByText("const value = 1;").closest("pre")!;
    expect(getComputedStyle(codeBlock).overflowX).toBe("auto");
  });

  it("从源码切回富文本时保留最新 Markdown", async () => {
    const user = userEvent.setup();
    render(<Harness initial="原文" />);

    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });
    await user.clear(source);
    await user.type(source, "## 切换后的标题");
    await user.click(screen.getByRole("tab", { name: "富文本" }));

    expect(screen.getByRole("textbox", { name: "富文本正文" })).toHaveTextContent("切换后的标题");
    expect(screen.getByTestId("markdown-value")).toHaveTextContent("## 切换后的标题");
  });

  it("父级替换草稿时同步当前富文本内容", async () => {
    const user = userEvent.setup();
    render(<ReplacementHarness />);

    await user.click(screen.getByRole("button", { name: "替换草稿" }));

    await waitFor(() => expect(screen.getByRole("textbox", { name: "富文本正文" })).toHaveTextContent("父级更新"));
    expect(screen.getByRole("heading", { level: 2, name: "父级更新" })).toBeInTheDocument();
  });

  it("富文本格式化会回写 Markdown 草稿", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const editor = screen.getByRole("textbox", { name: "富文本正文" });

    await user.click(editor);
    const paragraph = editor.querySelector("p")!;
    paragraph.textContent = "重要内容";
    fireEvent.input(editor, { data: "重要内容", inputType: "insertText" });
    await waitFor(() => expect(screen.getByTestId("markdown-value")).toHaveTextContent("重要内容"));
    const range = document.createRange();
    range.selectNodeContents(paragraph);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);
    expect(editor).toHaveTextContent("重要内容");
    await user.click(screen.getByRole("radio", { name: "粗体" }));

    await waitFor(() => expect(screen.getByTestId("markdown-value")).toHaveTextContent("**重要内容**"));
  });

  it("方向键移动模式标签焦点并由 Enter 激活", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const richTab = screen.getByRole("tab", { name: "富文本" });
    const markdownTab = screen.getByRole("tab", { name: "Markdown" });

    richTab.focus();
    await user.keyboard("{ArrowRight}");
    expect(markdownTab).toHaveFocus();
    expect(richTab).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(markdownTab).toHaveAttribute("aria-selected", "true");
  });

  it("方向键移动 Markdown 子模式标签焦点并由 Enter 激活", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole("tab", { name: "Markdown" }));
    const sourceTab = screen.getByRole("tab", { name: "源码" });
    const previewTab = screen.getByRole("tab", { name: "预览" });

    sourceTab.focus();
    await user.keyboard("{ArrowRight}");
    expect(previewTab).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(previewTab).toHaveAttribute("aria-selected", "true");
  });

  it("富文本无法解析时保留源码并提供返回动作", async () => {
    const user = userEvent.setup();
    render(<Harness initial="<custom>保留内容</custom>" />);

    expect(await screen.findByRole("alert")).toHaveTextContent("源码仍已完整保留");
    await user.click(screen.getByRole("button", { name: "返回源码" }));
    expect(screen.getByRole("textbox", { name: "Markdown 源码" })).toHaveValue("<custom>保留内容</custom>");
  });

  it("源码输入期间的中文组合事件不会切换模式", () => {
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Markdown" }));
    const source = screen.getByRole("textbox", { name: "Markdown 源码" });

    fireEvent.compositionStart(source);
    fireEvent.change(source, { target: { value: "中文草稿" } });
    fireEvent.keyDown(source, { key: "Enter", isComposing: true });
    fireEvent.compositionEnd(source);

    expect(source).toHaveValue("中文草稿");
    expect(screen.getByRole("tab", { name: "源码" })).toHaveAttribute("aria-selected", "true");
  });
});
