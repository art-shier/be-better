import { addDays } from "date-fns";
import type { AgentRun, AppData, CalendarEvent, Goal, Milestone, Note, RecordEntry, Task } from "./types";
import { atOffset, atToday, dateKey, toIso, todayStart } from "./dates";

const nowIso = () => new Date().toISOString();

function milestone(goalId: string, value: Omit<Milestone, "goalId" | "version" | "createdAt" | "updatedAt">): Milestone {
  const timestamp = value.completedAt ?? value.dueAt ?? nowIso();
  return { ...value, goalId, version: 1, createdAt: timestamp, updatedAt: timestamp };
}

const goals: Goal[] = [
  {
    id: "goal_product",
    title: "完成个人产品方案",
    why: "把生活管理的想法收敛成一份可验证、可开发的产品方案。",
    area: "工作",
    metricType: "milestone",
    targetValue: 5,
    currentValue: 2,
    unit: "个里程碑",
    startAt: toIso(addDays(todayStart(), -25)),
    dueAt: toIso(addDays(todayStart(), 18)),
    status: "active",
    health: "attention",
    milestones: [
      milestone("goal_product", { id: "ms_1", title: "完成问题与人群定义", completedAt: toIso(addDays(todayStart(), -18)), sortOrder: 1 }),
      milestone("goal_product", { id: "ms_2", title: "完成核心闭环", completedAt: toIso(addDays(todayStart(), -6)), sortOrder: 2 }),
      milestone("goal_product", { id: "ms_3", title: "补全首次使用流程", dueAt: toIso(addDays(todayStart(), 3)), sortOrder: 3 }),
      milestone("goal_product", { id: "ms_4", title: "完成可用性验证", dueAt: toIso(addDays(todayStart(), 11)), sortOrder: 4 }),
      milestone("goal_product", { id: "ms_5", title: "输出开发方案", dueAt: toIso(addDays(todayStart(), 18)), sortOrder: 5 }),
    ],
    version: 1,
    createdAt: toIso(addDays(todayStart(), -25)),
    updatedAt: toIso(addDays(todayStart(), -4)),
  },
  {
    id: "goal_run",
    title: "稳定跑完 10 公里",
    why: "建立可持续的体能习惯，而不是追求一次性的配速。",
    area: "健康",
    metricType: "habit",
    targetValue: 3,
    currentValue: 2,
    unit: "次 / 周",
    startAt: toIso(addDays(todayStart(), -35)),
    dueAt: toIso(addDays(todayStart(), 21)),
    status: "active",
    health: "normal",
    milestones: [],
    version: 1,
    createdAt: toIso(addDays(todayStart(), -35)),
    updatedAt: toIso(addDays(todayStart(), -2)),
  },
  {
    id: "goal_read",
    title: "年度阅读 24 本",
    why: "保持稳定输入，并留下真正改变行动的阅读笔记。",
    area: "成长",
    metricType: "numeric",
    targetValue: 24,
    currentValue: 15,
    unit: "本",
    startAt: toIso(addDays(todayStart(), -220)),
    status: "active",
    health: "normal",
    milestones: [],
    version: 1,
    createdAt: toIso(addDays(todayStart(), -220)),
    updatedAt: toIso(addDays(todayStart(), -7)),
  },
  {
    id: "goal_photo",
    title: "学习基础摄影",
    why: "通过每周主题练习建立构图和用光能力。",
    area: "成长",
    metricType: "project",
    targetValue: 100,
    currentValue: 28,
    unit: "%",
    startAt: toIso(addDays(todayStart(), -70)),
    status: "paused",
    health: "stalled",
    milestones: [],
    version: 1,
    createdAt: toIso(addDays(todayStart(), -70)),
    updatedAt: toIso(addDays(todayStart(), -15)),
  },
];

const task = (value: Omit<Task, "version" | "updatedAt">): Task => ({ ...value, version: 1, updatedAt: value.completedAt ?? value.createdAt });
const tasks: Task[] = [
  task({ id: "task_flow", title: "完成产品方案核心流程", status: "doing", priority: "important", estimateMinutes: 70, scheduledStart: toIso(atToday(9)), scheduledEnd: toIso(atToday(10, 10)), goalId: "goal_product", createdAt: toIso(addDays(todayStart(), -4)) }),
  task({ id: "task_dentist", title: "带上牙片和医保卡", status: "todo", priority: "important", estimateMinutes: 10, dueAt: toIso(atToday(14, 10)), goalId: undefined, createdAt: toIso(addDays(todayStart(), -1)) }),
  task({ id: "task_interviews", title: "整理用户访谈记录", status: "done", priority: "normal", estimateMinutes: 40, goalId: "goal_product", createdAt: toIso(addDays(todayStart(), -3)), completedAt: toIso(atToday(8, 46)) }),
  task({ id: "task_read", title: "阅读《设计中的设计》20 页", status: "todo", priority: "normal", estimateMinutes: 25, goalId: "goal_read", createdAt: toIso(addDays(todayStart(), -1)) }),
  task({ id: "task_weekly", title: "准备本周复盘", status: "todo", priority: "normal", estimateMinutes: 20, dueAt: toIso(atOffset(3, 17)), createdAt: nowIso() }),
];

const event = (id: string, title: string, day: number, startH: number, startM: number, duration: number, kind: CalendarEvent["kind"], extras: Partial<CalendarEvent> = {}): CalendarEvent => ({
  id, title, startAt: toIso(atOffset(day, startH, startM)), endAt: toIso(new Date(atOffset(day, startH, startM).getTime() + duration * 60_000)), reminderMinutes: [10], timezone: "Asia/Shanghai", kind, version: 1, createdAt: nowIso(), updatedAt: nowIso(), ...extras,
});

const events: CalendarEvent[] = [
  event("event_focus", "产品方案", 0, 9, 0, 70, "focus", { goalId: "goal_product" }),
  event("event_weekly", "项目周会", 0, 10, 30, 45, "fixed", { location: "腾讯会议" }),
  event("event_dentist", "牙医复诊", 0, 14, 30, 50, "personal", { location: "瑞尔齿科", reminderMinutes: [20] }),
  event("event_run", "轻松跑 5 公里", 0, 18, 10, 35, "health", { goalId: "goal_run" }),
  event("event_sync", "团队同步", -3, 10, 0, 45, "fixed"),
  event("event_deep", "方案专注块", -2, 12, 0, 70, "focus", { goalId: "goal_product" }),
  event("event_strength", "力量训练", -1, 16, 0, 40, "health", { goalId: "goal_run" }),
  event("event_review", "产品评审", 1, 10, 20, 60, "fixed"),
  event("event_dinner", "朋友聚餐", 2, 13, 50, 60, "personal"),
  event("event_week_review", "周复盘", 3, 16, 50, 50, "focus"),
];

const record = (value: Omit<RecordEntry, "version" | "createdAt" | "updatedAt">): RecordEntry => ({ ...value, version: 1, createdAt: value.occurredAt, updatedAt: value.occurredAt });
const records: RecordEntry[] = [
  record({ id: "record_morning", rawText: "昨晚睡得稍晚，但起床后的状态比预想中好。上午先不要被消息打断。", kind: "status", occurredAt: toIso(atToday(8, 42)), energy: 3, tags: ["精力 3/5", "晨间状态"] }),
  record({ id: "record_idea", rawText: "产品首页不应该只是数据仪表盘，而要先回答“今天做什么”。", kind: "idea", occurredAt: toIso(atToday(10, 18)), tags: ["产品思考", "待整理为笔记"] }),
  record({ id: "record_done", rawText: "完成访谈记录整理，发现“计划太满”是最常出现的挫败来源。", kind: "completion", occurredAt: toIso(atToday(11, 35)), tags: ["已完成", "个人产品方案"] }),
  record({ id: "record_inbox_1", rawText: "周五下午三点看牙", kind: "inbox", occurredAt: toIso(atToday(12, 6)), tags: ["待确认"] }),
  record({ id: "record_inbox_2", rawText: "明早跑 5 公里", kind: "inbox", occurredAt: toIso(atToday(11, 48)), tags: ["待确认"] }),
];

const notes: Note[] = [
  { id: "note_loop", title: "生活管理产品的核心闭环", bodyMarkdown: "从目标出发，将真实时间约束、个人精力和历史执行记录组合成今天真正可行的计划。\n\n首页应先回答今天做什么，再提供更深的回顾与统计。", tags: ["产品", "核心闭环"], category: "产品思考", linkedEntityIds: ["goal_product"], version: 1, createdAt: toIso(addDays(todayStart(), -10)), updatedAt: toIso(atToday(10, 26)) },
  { id: "note_design", title: "《设计中的设计》", bodyMarkdown: "设计不是制造漂亮的物品，而是重新发现事物之间关系的一种方式。", tags: ["设计", "阅读"], category: "阅读笔记", linkedEntityIds: ["goal_read"], version: 1, createdAt: toIso(addDays(todayStart(), -2)), updatedAt: toIso(addDays(todayStart(), -1)) },
  { id: "note_run", title: "10 公里训练阶段复盘", bodyMarkdown: "下一阶段优先保持轻松跑心率，不急于增加强度。晚间训练容易被推迟，尽量安排到晚饭前。", tags: ["跑步", "复盘"], category: "健康训练", linkedEntityIds: ["goal_run"], version: 1, createdAt: toIso(addDays(todayStart(), -5)), updatedAt: toIso(addDays(todayStart(), -3)) },
  { id: "note_energy", title: "低精力日的任务选择", bodyMarkdown: "把任务分为创造、沟通和整理三种。精力不足时不靠意志力硬顶，而是切换到准备好的替代清单。", tags: ["方法", "精力"], category: "生活方法", linkedEntityIds: [], version: 1, createdAt: toIso(addDays(todayStart(), -12)), updatedAt: toIso(addDays(todayStart(), -7)) },
];

const agentRun: AgentRun = {
  id: "run_week_plan",
  intent: "整理本周剩余计划，优先保证产品方案和跑步目标",
  status: "waiting",
  actionMode: "confirm",
  scope: ["目标 3 项", "任务 12 项", "本周日程"],
  startedAt: toIso(atToday(9, 12)),
  steps: [
    { id: "step_read", title: "读取授权数据", detail: "目标、未完成任务和本周日程", status: "done", meta: "18 个对象" },
    { id: "step_conflict", title: "检查时间与目标冲突", detail: "发现 1 个冲突、1 个停滞目标", status: "done", meta: "规则校验通过" },
    { id: "step_changes", title: "生成待确认变更", detail: "2 项变更，需要你的决定", status: "done", meta: "当前步骤" },
    { id: "step_apply", title: "写入并核验", detail: "确认后执行；失败不会产生部分写入", status: "pending", meta: "尚未开始" },
  ],
  changes: [
    { id: "change_run", type: "reschedule-task", entityId: "task_run_suggested", title: "调整“轻松跑 5 公里”的时间", before: "周四 20:30", after: "周四 18:10", reason: "近两周晚间训练更容易被推迟，18:10 与固定日程无冲突。", status: "pending", sourceRefs: [{ id: "goal_run", kind: "goal", label: "稳定跑完 10 公里" }, { id: "event_run", kind: "event", label: "轻松跑 5 公里" }] },
    { id: "change_focus", type: "create-task", title: "创建“产品方案核心流程”专注任务", after: "今天 09:00—10:10 · 70 分钟", reason: "该目标已 4 天未推进，上午是深度任务完成率最高的时段。", status: "pending", sourceRefs: [{ id: "goal_product", kind: "goal", label: "完成个人产品方案" }, { id: "record_morning", kind: "record", label: "晨间精力记录" }] },
  ],
};

export function createSeedData(): AppData {
  return {
    version: 1,
    goals,
    tasks,
    events,
    records,
    notes,
    reviews: [],
    agentRuns: [agentRun],
    audit: [
      { id: "audit_1", actor: "agent", action: "生成周计划建议", entityRefs: ["run_week_plan"], createdAt: toIso(atToday(9, 12)) },
      { id: "audit_2", actor: "user", action: "整理 4 条会议记录", entityRefs: ["task_interviews"], createdAt: toIso(atOffset(-1, 17, 40)) },
      { id: "audit_3", actor: "agent", action: "检查目标健康度", entityRefs: goals.slice(0, 3).map((goal) => goal.id), createdAt: toIso(atOffset(-2, 9, 20)) },
    ],
    settings: { schemaVersion: 1, version: 1, updatedAt: nowIso(), energy: 3, aiEnabled: true, remindersEnabled: false, onboardingCompleted: true, focusAreas: ["工作", "健康", "成长"], dataMode: "local", localOnly: true, permissions: { goals: true, calendar: true, records: true, privateNotes: false } },
  };
}

export function createEmptyData(): AppData {
  return {
    version: 1,
    goals: [],
    tasks: [],
    events: [],
    records: [],
    notes: [],
    reviews: [],
    agentRuns: [],
    audit: [],
    settings: { schemaVersion: 1, version: 0, updatedAt: nowIso(), energy: 3, aiEnabled: true, remindersEnabled: false, onboardingCompleted: false, focusAreas: [], dataMode: "local", localOnly: true, permissions: { goals: true, calendar: true, records: true, privateNotes: false } },
  };
}

export const seedTodayKey = dateKey(new Date());
