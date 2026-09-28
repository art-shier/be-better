---
version: alpha
name: "日序"
description: "面向个人日常规划与记录的冷静工作台，以清晰层级和克制的纸面质感承载高频任务。"
colors:
  canvas: "#f4f6f8"
  surface: "#ffffff"
  surface-muted: "#f7f9fa"
  ink: "#18232e"
  ink-secondary: "#596775"
  ink-muted: "#8995a1"
  primary: "#52758a"
  primary-deep: "#3f6277"
  primary-soft: "#e7eef2"
  accent: "#9b6954"
  danger: "#9e5755"
  success: "#4f7869"
  border: "#dde3e8"
typography:
  body:
    fontFamily: "Inter, 'Segoe UI Variable Text', 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif"
  display:
    fontFamily: "'Aptos Display', 'Segoe UI Variable Display', 'PingFang SC', sans-serif"
  mono:
    fontFamily: "'SFMono-Regular', Consolas, monospace"
rounded:
  DEFAULT: "6px"
  control: "6px"
  panel: "8px"
  structure: "10px"
spacing:
  control-height: "44px"
  page-inline: "34px"
  page-max: "1480px"
components:
  button: {}
  input: {}
  card: {}
  dialog: {}
  tag: {}
---

# 日序 Design System

## Overview

### Creative North Star

界面像一本整理良好的个人工作台账：浅灰画布、白色工作面、清楚的墨色层级和少量蓝灰标记。它不是娱乐化的卡片墙，也不是高饱和的效率仪表盘。

### Product context and register

- **Audience and primary job:** 个人用户在同一处安排目标、任务、日程，并沉淀记录与笔记。
- **Target market(s) and evidence:** 当前代码和中文产品文案仅证明中文界面，不推断特定国家市场。
- **Locale(s) and language policy:** 当前产品文案为简体中文，技术标识可保留英文；新增文案沿用自然、直接的中文动作词。
- **Usage scene:** 桌面端高频整理为主，同时支持窄屏快速查看和编辑。
- **Register:** 产品型工作台；任务清晰度和跨页面一致性优先。
- **Memorable signature:** 深色结构面与低饱和蓝灰操作色形成“台账封面 / 纸张内页”的层次。
- **Restraint:** CRUD、校验、确认和空状态保持安静、熟悉，不额外引入装饰或大幅动效。
- **Anti-references:** 避免高饱和渐变、玻璃拟态、过度圆角、营销页式大数字和无意义装饰图标。
- **Token ownership/runtime mapping:** 成熟代码库采用运行时权威模型；`apps/web/src/styles.css` 中的 CSS 变量是运行时唯一来源，本文件镜像其稳定语义。共享组件消费变量，不在功能组件复制色值。

## Colors

`canvas` 承载页面背景，`surface` / `surface-muted` 区分主要与次要工作面。正文使用 `ink`，说明和辅助信息依次使用 `ink-secondary`、`ink-muted`。`primary` 只表达主动作、选择和焦点；`danger` 只表达删除与错误；状态不得只靠颜色传达。当前仅支持浅色主题，高对比模式保留系统可操作性。

## Typography

正文使用包含中文回退的无衬线栈；页面大标题使用 display 栈；时间、快捷键和技术数据使用 mono 栈。中文正文不使用全大写或斜体，控件文案用明确动词，密集辅助信息仍须保持可读行高。

## Layout

页面最大宽度 1480px，桌面内边距 34px；现有 1120px、860px、580px 断点为响应式权威。桌面工具栏可横排，窄屏转为自然换行或全宽操作。弹窗内容在自身内部滚动，标题和操作保持可达，不通过固定页面高度制造嵌套滚动。应用拥有的滚动区域统一继承可见的细滚动条基线，局部类只负责稳定沟槽等几何例外。

## Elevation & Depth

静态层级主要依靠表面色和 1px 边框；浮层使用 `--shadow-float`，顶部导航仅使用极轻阴影。不要给普通列表项叠加卡片阴影。Modal 遮罩使用克制的深色透明层与模糊，Toast 始终位于其他浮层之上。

## Shapes

输入与按钮使用 6px 圆角，面板使用 8px，主要结构与弹窗使用 10px。标签是紧凑元数据，可使用同一控制圆角或小型胶囊，但不能伪装成主要按钮。

## Components

### Foundational visual states

可操作元素必须包含默认、hover、focus-visible、active、disabled/busy 状态；焦点使用已有蓝灰半透明 3px 外框。错误在字段附近显示文本并保持输入值。快速本地操作不闪烁加载器。

### Buttons and actions

安全主动作使用 `.button.primary`，次要与取消使用 `.button.secondary`，页面内删除入口使用低强调危险样式，最终删除确认使用明确危险动作。按钮最小高度 44px，图标只辅助文本，不替代动作名称。

### Navigation and data display

标签使用真实名称和引用数量表达用途。静态标签不可呈现为可点击控件；编辑、删除必须由独立语义按钮触发。长名称允许换行或截断时提供完整访问路径。

### Forms and overlays

复用 `Modal`、表单字段、Toast 与应用 Store。产品表单使用应用内校验，错误与输入通过 `aria-invalid`、`aria-describedby` 关联。Modal 负责焦点圈定、Escape、背景隔离和触发器焦点恢复；确认流程在同一浮层内切换，不嵌套对话框。

### Note content editor

Markdown 是视觉与数据骨架。富文本、源码和预览共用一个有边框的工作面；模式控件沿用安静的分段控件语法，代码使用 mono 字体栈，长代码和表格只在自身内部滚动。编辑器不引入新的色板或圆角 token；桌面编辑面最小高度为 320px，580px 及以下为 240px。

### Iconography

统一使用 Lucide 线性图标，常规尺寸 16–18px。新增、编辑、删除等图标与文本共同出现；图标按钮必须有中文可访问名称。

### Motion

动效仅说明状态变化，沿用 150–200ms 的淡入或轻微位移；`prefers-reduced-motion` 下压缩为近乎即时，不依赖移动传达结果。

### Content and data visualization

语气直接、具体：使用“添加标签”“保存名称”“删除标签”，反馈与触发动作保持同一词汇。不向用户暴露后端原始错误。

## Do's and Don'ts

- **Do:** 复用 CSS 变量、Modal、Toast 和既有 44px 控件节奏。
- **Do:** 在标签改名、删除后同步更新笔记与记录中的可见引用。
- **Don't:** 为标签管理另造浮层、通知系统或一组硬编码颜色。
- **Don't:** 使用浏览器 `alert`、`confirm`、`prompt`，或只用红色表达删除风险。
