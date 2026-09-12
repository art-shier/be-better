# Markdown-first 富文本编辑器设计

## 背景与目标

笔记正文当前由 `EntityDialog` 中的普通 `<textarea>` 直接编辑，并以 `Note.bodyMarkdown` / PostgreSQL `notes.body_markdown` 保存。用户需要同时获得 Markdown 源码、Markdown 预览和富文本编辑能力。

本次改造以 Markdown 作为唯一内容源，在不修改数据库、API、离线缓存和同步 Mutation 格式的前提下增加三种编辑表面。现有笔记必须可以直接打开、切换和保存。

## 已确认的产品决策

- Markdown 是唯一持久化格式，不保存平行 HTML 或编辑器私有 JSON。
- 顶层模式为“富文本”和“Markdown”。
- Markdown 模式内提供“源码”和“预览”。
- 富文本仅开放 Markdown 可稳定表达的格式。
- 预览不执行笔记中的原始 HTML。
- 模式切换只更新当前草稿；仍由“保存笔记”统一提交。

## 方案选择

采用 `@mdxeditor/editor` 作为 Markdown-first 富文本编辑器，采用 `react-markdown` 与 `remark-gfm` 作为只读预览。

选择理由：

- `@mdxeditor/editor` 原生以 Markdown 输入输出，避免维护 HTML ⇄ Markdown 双向转换器。
- 当前版本支持 React 19，与项目运行时兼容。
- Lexical 提供成熟的选区、撤销、粘贴和输入法行为，风险低于自行实现 `contenteditable`。
- `react-markdown` 默认把原始 HTML 当作文本而不是执行；不接入 `rehype-raw`，保持安全边界清楚。

未选择 TipTap，是因为仍需自行维护 Markdown 扩展配置和模式同步；未选择自制编辑器，是因为选区、撤销、粘贴、IME 和无障碍成本过高。

## 信息架构与交互

### 顶层模式

正文区域顶部使用项目已有的分段控件视觉，提供：

- **富文本**：默认模式，显示格式工具栏和所见即所得正文。
- **Markdown**：显示二级“源码 / 预览”切换。

控件使用 `role="tablist"`、`role="tab"`、`aria-selected` 和关联的 `tabpanel`。左右方向键遵循标签页键盘模型，Tab 进入当前面板。

### 富文本模式

首版工具栏提供：

- 撤销、重做；
- 正文、一级至三级标题；
- 粗体、斜体；
- 无序列表、有序列表；
- 引用；
- 链接；
- 行内代码、代码块；
- 分隔线。

不提供字体、字号、文字颜色、背景色、下划线、对齐和任意 HTML，因为这些格式不能稳定映射到统一 Markdown。

### Markdown 源码

源码使用带可见标签的原生 `<textarea>`，字体采用项目 mono token，保留换行和所有 Markdown 字符。输入直接更新父组件持有的 canonical Markdown 草稿。Textarea 禁止拖拽改变布局，并提供足够的默认高度。

### Markdown 预览

预览通过 `react-markdown` 渲染 canonical Markdown，并启用 `remark-gfm` 支持表格、删除线、任务列表和自动链接。预览区域为只读 `tabpanel`，空内容显示“还没有正文，切换到源码或富文本开始输入”。

链接默认添加安全的外部链接属性；不接入 `rehype-raw`，`<script>`、事件属性和原始 HTML 不会进入可执行 DOM。代码块、表格、引用、标题、列表和图片具有与日序设计系统一致的样式及窄屏溢出策略。

## 组件边界

### 新增 `NoteContentEditor`

从 `EntityDialog.tsx` 提取独立组件：

```ts
interface NoteContentEditorProps {
  value: string;
  onChange(value: string): void;
}
```

组件负责：

- 顶层模式和 Markdown 子模式状态；
- MDXEditor 插件与工具栏配置；
- canonical Markdown 在各表面之间同步；
- 标签页键盘行为和可访问名称；
- 空预览状态。

组件不负责保存、Toast、笔记元数据、标签或实体关联。

### `NoteEditor`

`NoteEditor` 继续拥有标题、分类、标签、关联目标和 `body` 草稿。正文位置改为：

```tsx
<NoteContentEditor value={body} onChange={setBody} />
```

保存时仍把 `body` 写入 `note.bodyMarkdown`，因此 Store、离线队列、API 和 PostgreSQL 无需改变。

## 同步策略

- `NoteEditor` 的 React state 是弹窗生命周期内的 canonical Markdown。
- 源码输入每次变更立即更新 canonical state。
- 富文本编辑器通过 `onChange(markdown)` 更新 canonical state。
- 从源码或预览切回富文本时，以最新 canonical Markdown 调用编辑器的 `setMarkdown` 或通过受控重建同步内容。
- 富文本编辑器内部产生的等价格式规范化可以更新草稿，但不得触发 Store Mutation。
- 更换编辑目标时重置正文和模式，避免上一篇笔记状态泄漏。

## 错误与恢复

- 无法解析的普通 Markdown 仍可在源码模式编辑和保存；富文本模式显示明确的内联错误，并提供“返回源码”动作，不清空草稿。
- 链接输入和编辑器浮层必须留在共享 Modal 的焦点范围内，并在 Escape 后返回触发按钮。
- 切换模式不丢弃未保存内容。
- 关闭笔记弹窗沿用现有取消语义；本次不扩大为全产品未保存变更守卫。

## 样式与响应式

- 编辑器使用既有 `--surface`、`--surface-2`、`--line`、`--ink-*`、`--primary` 和圆角 token，不复制品牌色。
- 工具栏允许横向滚动或自然换行，不隐藏格式动作。
- 编辑区桌面最小高度约 320px；窄屏最小高度约 240px，并随 Modal 内部滚动。
- 表格和代码块在自身容器横向滚动，不造成整个页面横向滚动。
- 焦点使用现有 3px 蓝灰外框；只读预览不伪装成输入框。
- 减少动态效果时关闭编辑器和模式切换的非必要动画。

## 依赖与安全

新增运行时依赖：

- `@mdxeditor/editor`
- `react-markdown`
- `remark-gfm`

依赖使用精确版本写入锁文件。渲染路径不启用原始 HTML，不使用 `dangerouslySetInnerHTML`，不执行 MDX JSX、表达式或导入语句。首版只启用普通 Markdown 与 GFM 能力。

## 测试与验收

- 旧 `bodyMarkdown` 笔记打开时富文本内容可见。
- 默认显示富文本模式及约定工具栏。
- 切换到 Markdown 源码后显示原始 Markdown，编辑内容后再切回富文本不会丢失。
- Markdown 预览正确渲染标题、粗体、列表、链接、引用、代码块、表格和任务列表。
- 原始 HTML 与脚本不会生成可执行节点。
- 在富文本中输入和应用至少一种块格式、一种行内格式后，保存的 `bodyMarkdown` 包含对应 Markdown。
- 模式切换期间不产生 Store Mutation；点击“保存笔记”后只产生一次笔记 Mutation。
- 中文 IME 组合输入不会触发模式切换、保存或截断文字。
- 键盘可切换模式、遍历工具栏并返回正文；焦点不会逃出 Modal。
- 320–580px 视口下工具栏、源码和预览均可使用，无页面级横向滚动。

## 非目标

- 不支持协同编辑、评论、修订记录或自动保存。
- 不引入 HTML/JSON 正文列或数据库迁移。
- 不执行 MDX 组件、JavaScript 表达式或用户 HTML。
- 不在本次实现图片上传、附件管理、语法高亮主题选择或自定义字体颜色。
- 不改造记录、目标、任务和日程编辑器。
