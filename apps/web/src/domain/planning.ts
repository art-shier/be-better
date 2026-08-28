import { addMinutes, format, isSameDay, startOfDay } from "date-fns";
import type { AppData, Task } from "./types";

export interface TodayPlanItem {
  id: string;
  taskId: string;
  title: string;
  startAt: string;
  endAt: string;
  detail: string;
  evidence: string;
  confidence: number;
}

export interface DeferredPlanItem {
  taskId: string;
  title: string;
  reason: string;
}

interface Window { start: number; end: number }

const overlaps = (left: Window, right: Window) => left.start < right.end && left.end > right.start;
const roundUp = (value: Date, minutes: number) => {
  const result = new Date(value);
  result.setSeconds(0, 0);
  const remainder = result.getMinutes() % minutes;
  if (remainder || result.getTime() <= value.getTime()) result.setMinutes(result.getMinutes() + (remainder ? minutes - remainder : minutes));
  return result;
};

function rankTask(task: Task, data: AppData, now: Date): number {
  const goal = data.goals.find((item) => item.id === task.goalId);
  const dueSoon = task.dueAt ? Math.max(0, 12 - Math.floor((new Date(task.dueAt).getTime() - now.getTime()) / 86_400_000)) : 0;
  return (task.priority === "important" ? 40 : 0) + (goal?.health === "stalled" ? 24 : goal?.health === "attention" ? 16 : goal ? 8 : 0) + dueSoon;
}

export function buildTodayPlan(data: AppData, now = new Date()): { plans: TodayPlanItem[]; deferred: DeferredPlanItem[] } {
  const dayStart = startOfDay(now);
  const at = (hour: number) => new Date(dayStart.getTime() + hour * 60 * 60_000);
  const dayEnd = at(20).getTime();
  let cursor = Math.max(at(8).getTime(), roundUp(now, 15).getTime());
  const reservations: Window[] = data.events
    .filter((event) => isSameDay(new Date(event.startAt), now))
    .map((event) => ({ start: new Date(event.startAt).getTime() - 15 * 60_000, end: new Date(event.endAt).getTime() + 15 * 60_000 }));

  const candidates = data.tasks
    .filter((task) => (task.status === "todo" || task.status === "doing") && (!task.scheduledStart || isSameDay(new Date(task.scheduledStart), now) || Boolean(task.dueAt && isSameDay(new Date(task.dueAt), now))))
    .sort((left, right) => {
      const leftFuture = left.scheduledStart && isSameDay(new Date(left.scheduledStart), now) && new Date(left.scheduledStart).getTime() > now.getTime();
      const rightFuture = right.scheduledStart && isSameDay(new Date(right.scheduledStart), now) && new Date(right.scheduledStart).getTime() > now.getTime();
      if (leftFuture && rightFuture) return left.scheduledStart!.localeCompare(right.scheduledStart!);
      if (leftFuture) return -1;
      if (rightFuture) return 1;
      return rankTask(right, data, now) - rankTask(left, data, now) || (left.dueAt ?? "9999").localeCompare(right.dueAt ?? "9999") || left.createdAt.localeCompare(right.createdAt);
    });

  const plans: TodayPlanItem[] = [];
  const deferred: DeferredPlanItem[] = [];

  candidates.forEach((task) => {
    if (plans.length >= 3) {
      deferred.push({ taskId: task.id, title: task.title, reason: "今日重点控制在 3 项，先保留在任务列表中。" });
      return;
    }

    const duration = Math.max(5, Math.min(120, task.estimateMinutes || 30));
    const durationMs = duration * 60_000;
    const existingStart = task.scheduledStart ? new Date(task.scheduledStart).getTime() : 0;
    const existingEnd = task.scheduledEnd ? new Date(task.scheduledEnd).getTime() : existingStart + durationMs;
    const existingWindow = { start: existingStart, end: existingEnd };
    const canKeepExisting = Boolean(existingStart > now.getTime() && isSameDay(new Date(existingStart), now) && existingEnd <= dayEnd && !reservations.some((window) => overlaps(existingWindow, window)));

    let start = canKeepExisting ? existingStart : cursor;
    if (!canKeepExisting) {
      while (start + durationMs <= dayEnd && reservations.some((window) => overlaps({ start, end: start + durationMs }, window))) {
        const collision = reservations.filter((window) => overlaps({ start, end: start + durationMs }, window)).sort((a, b) => a.end - b.end)[0];
        start = Math.max(start + 15 * 60_000, collision.end);
      }
    }

    if (start + durationMs > dayEnd) {
      deferred.push({ taskId: task.id, title: task.title, reason: "固定日程与缓冲后没有足够的连续时间。" });
      return;
    }

    const end = start + durationMs;
    reservations.push({ start, end });
    reservations.sort((a, b) => a.start - b.start);
    cursor = Math.max(cursor, end + 10 * 60_000);
    const goal = data.goals.find((item) => item.id === task.goalId);
    const moved = Boolean(task.scheduledStart && !canKeepExisting);
    const evidence = [goal ? `目标：${goal.title}` : "独立任务", task.priority === "important" ? "优先级：重要" : `预计：${duration} 分钟`, task.dueAt ? `截止：${format(new Date(task.dueAt), "M 月 d 日 HH:mm")}` : `当前精力：${data.settings.energy} / 5`, moved ? "原时间已过或与固定日程冲突" : canKeepExisting ? "保留已有安排" : "匹配今日可用时间"].join(" · ");
    plans.push({ id: task.id, taskId: task.id, title: task.title, startAt: new Date(start).toISOString(), endAt: new Date(end).toISOString(), detail: `${duration} 分钟${goal ? ` · 推进“${goal.title}”` : " · 独立任务"}`, evidence, confidence: Math.min(96, 76 + (task.priority === "important" ? 9 : 0) + (goal ? 5 : 0) + (task.dueAt ? 4 : 0) - (moved ? 3 : 0)) });
  });

  return { plans, deferred };
}
