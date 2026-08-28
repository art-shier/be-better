import { ChevronLeft, ChevronRight, Plus } from "lucide-react";
import { addWeeks, differenceInMinutes, endOfWeek, format, isSameDay, startOfWeek } from "date-fns";
import { useMemo, useState } from "react";
import type { CalendarEvent } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";

const eventClass: Record<CalendarEvent["kind"], string> = { focus: "focus", fixed: "fixed", health: "health", personal: "personal" };

export function CalendarPage() {
  const { data } = useAppStore();
  const { editEvent } = useUi();
  const [weekOffset, setWeekOffset] = useState(0);
  const weekStart = startOfWeek(addWeeks(new Date(), weekOffset), { weekStartsOn: 1 });
  const weekEnd = endOfWeek(weekStart, { weekStartsOn: 1 });
  const days = Array.from({ length: 7 }, (_, index) => new Date(weekStart.getTime() + index * 86_400_000));
  const weekEvents = useMemo(() => data.events.filter((event) => new Date(event.startAt) >= weekStart && new Date(event.startAt) <= weekEnd), [data.events, weekEnd, weekStart]);
  const dayEvents = (day: Date) => weekEvents.filter((event) => isSameDay(new Date(event.startAt), day));
  const topFor = (event: CalendarEvent) => Math.max(0, (new Date(event.startAt).getHours() * 60 + new Date(event.startAt).getMinutes() - 8 * 60) / (12 * 60) * 100);
  const heightFor = (event: CalendarEvent) => Math.max(7, differenceInMinutes(new Date(event.endAt), new Date(event.startAt)) / (12 * 60) * 100);

  return <div className="view-page"><header className="view-head"><div><p className="eyebrow">{format(weekStart,"M 月 d 日")}—{format(weekEnd,"M 月 d 日")}</p><h1>先看真实可用的时间。</h1><p>固定日程、缓冲和任务时间块放在同一张周视图中。</p></div><div className="head-actions"><div className="week-controls"><button type="button" aria-label="上一周" onClick={() => setWeekOffset((value) => value - 1)}><ChevronLeft size={17} /></button><button type="button" onClick={() => setWeekOffset(0)}>今天</button><button type="button" aria-label="下一周" onClick={() => setWeekOffset((value) => value + 1)}><ChevronRight size={17} /></button></div><button className="button primary" type="button" onClick={() => editEvent()}><Plus size={17} />添加日程</button></div></header>
    <section className="panel week-panel"><div className="week-scroll"><div className="week-board"><div className="week-head"><div /><>{days.map((day) => <div className={`week-day ${isSameDay(day,new Date()) ? "today" : ""}`} key={day.toISOString()}><span>{["周日","周一","周二","周三","周四","周五","周六"][day.getDay()]}</span><strong>{format(day,"d")}</strong></div>)}</></div><div className="week-body"><div className="time-axis">{[8,10,12,14,16,18,20].map((hour) => <span key={hour}>{String(hour).padStart(2,"0")}:00</span>)}</div>{days.map((day) => <div className={`day-column ${isSameDay(day,new Date()) ? "today" : ""}`} key={day.toISOString()}>{dayEvents(day).map((event) => <button key={event.id} data-entity-id={event.id} className={`calendar-event ${eventClass[event.kind]}`} style={{ top: `${topFor(event)}%`, height: `${heightFor(event)}%` }} type="button" onClick={() => editEvent(event)}><strong>{event.title}</strong><span>{format(new Date(event.startAt),"HH:mm")}</span></button>)}</div>)}</div></div></div></section>
    <div className="calendar-legend"><span><i className="focus" />专注块</span><span><i className="fixed" />固定日程</span><span><i className="health" />健康</span><span><i className="personal" />个人</span></div>
  </div>;
}
