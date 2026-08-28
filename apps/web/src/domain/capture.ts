import { addDays, getDay, setHours, setMinutes } from "date-fns";
import { createId } from "./ids";
import type { CaptureDraft, EntityKind, Goal } from "./types";

function nextWeekday(target: number): Date {
  const now = new Date();
  const delta = (target - getDay(now) + 7) % 7 || 7;
  return addDays(now, delta);
}

function parseTime(text: string, base: Date): Date {
  const withPeriod = text.match(/(下午|晚上|上午|早上|中午)\s*(\d{1,2})(?:\s*(?:[:点时])\s*(\d{1,2})?)?/);
  const explicitClock = text.match(/(\d{1,2})\s*(?::\s*(\d{1,2})|点|时)/);
  const period = withPeriod?.[1] ?? (text.match(/下午|晚上|上午|早上|中午|明早/)?.[0]);
  let hour = withPeriod ? Number(withPeriod[2]) : explicitClock ? Number(explicitClock[1]) : /明早|早上/.test(text) ? 7 : /中午/.test(text) ? 12 : /下午/.test(text) ? 14 : /晚上/.test(text) ? 19 : 9;
  const minute = Number(withPeriod?.[3] ?? explicitClock?.[2] ?? (/明早/.test(text) ? 30 : 0));
  if ((period === "下午" || period === "晚上") && hour < 12) hour += 12;
  if (period === "中午" && hour < 11) hour += 12;
  return setMinutes(setHours(base, hour), minute);
}

export function parseCapture(rawText: string, goals: Goal[], preferred?: EntityKind): CaptureDraft {
  const text = rawText.trim();
  const now = new Date();
  let kind: EntityKind = preferred ?? "record";
  let startAt: string | undefined;
  let endAt: string | undefined;
  let explanation = "先作为原始记录保存，之后可以继续整理。";
  let confidence = 0.56;

  const weekdayMap: Record<string, number> = { 周日: 0, 周一: 1, 周二: 2, 周三: 3, 周四: 4, 周五: 5, 周六: 6 };
  const weekday = Object.keys(weekdayMap).find((label) => text.includes(label));
  const tomorrow = /明早|明天/.test(text);
  const hasTime = Boolean(weekday || tomorrow || /\d{1,2}[点时:]|上午|下午|晚上/.test(text));
  const eventIntent = /会议|复诊|看牙|聚餐|约|日程|出发/.test(text);
  const taskIntent = /记得|完成|整理|跑|阅读|买|提交|跟进|训练/.test(text);
  const noteIntent = /笔记|想法|灵感|可以|方法|发现/.test(text);
  const goalIntent = /目标|今年|每周|坚持|达到/.test(text);
  const statusIntent = /状态|精力|心情|有点困|很困|疲惫/.test(text);

  if (!preferred) {
    if (eventIntent && hasTime) kind = "event";
    else if (taskIntent) kind = "task";
    else if (goalIntent) kind = "goal";
    else if (noteIntent && text.length > 18) kind = "note";
  }

  if (hasTime) {
    const base = weekday ? nextWeekday(weekdayMap[weekday]) : tomorrow ? addDays(now, 1) : now;
    const parsed = parseTime(text, base);
    startAt = parsed.toISOString();
    endAt = new Date(parsed.getTime() + 45 * 60_000).toISOString();
    confidence = eventIntent ? 0.91 : 0.78;
    explanation = `识别到时间信息，建议创建${kind === "event" ? "日程" : "带时间的任务"}草稿。`;
  } else if (kind !== "record") {
    confidence = 0.76;
    explanation = `根据内容中的动作词，建议整理为${({ task: "任务", note: "笔记", goal: "目标", event: "日程", record: "记录", review: "复盘" } as Record<EntityKind, string>)[kind]}。`;
  }

  const relatedGoal = goals.find((goal) => (text.includes("跑") && goal.area === "健康") || text.includes(goal.title.slice(0, 4)));
  const statusScore = text.match(/(?:状态|精力|心情)\s*(\d{1,2})(?:\s*分)?/);
  const normalizedScore = statusScore ? Math.max(1, Math.min(5, Math.round(Number(statusScore[1]) > 5 ? Number(statusScore[1]) / 2 : Number(statusScore[1])))) : undefined;

  return {
    rawText: text,
    kind,
    title: text.replace(/[。！!？?]$/, ""),
    occurredAt: now.toISOString(),
    startAt,
    endAt,
    estimateMinutes: /跑|训练/.test(text) ? 35 : kind === "task" ? 30 : undefined,
    goalId: relatedGoal?.id,
    confidence,
    explanation,
    recordKind: kind === "record" && statusIntent ? "status" : kind === "record" ? "idea" : undefined,
    mood: /心情/.test(text) ? normalizedScore : undefined,
    energy: /状态|精力|困|疲惫/.test(text) ? normalizedScore : undefined,
  };
}

export function draftId(draft: CaptureDraft): string {
  return createId(draft.kind);
}
