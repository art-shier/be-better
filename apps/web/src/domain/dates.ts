import { addDays, endOfDay, format, isSameDay, setHours, setMinutes, startOfDay } from "date-fns";

export const toIso = (date: Date) => date.toISOString();
export const todayStart = () => startOfDay(new Date());
export const todayEnd = () => endOfDay(new Date());
export const atToday = (hour: number, minute = 0) => setMinutes(setHours(todayStart(), hour), minute);
export const atOffset = (dayOffset: number, hour: number, minute = 0) => setMinutes(setHours(addDays(todayStart(), dayOffset), hour), minute);
export const isToday = (value?: string) => Boolean(value && isSameDay(new Date(value), new Date()));
export const formatTime = (value: string) => format(new Date(value), "HH:mm");
export const formatDate = (value: string) => format(new Date(value), "M 月 d 日");
export const formatDateTime = (value: string) => format(new Date(value), "M 月 d 日 HH:mm");
export const dateKey = (value: Date | string) => format(typeof value === "string" ? new Date(value) : value, "yyyy-MM-dd");

export function weekdayLabel(date = new Date()): string {
  return ["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"][date.getDay()];
}

export function greeting(date = new Date()): string {
  const hour = date.getHours();
  if (hour < 6) return "夜深了，先照顾好明天的自己。";
  if (hour < 12) return "早上好，把最清醒的时间留给重要的事。";
  if (hour < 18) return "下午好，把注意力留给重要的事。";
  return "晚上好，给今天一个清楚的收尾。";
}
