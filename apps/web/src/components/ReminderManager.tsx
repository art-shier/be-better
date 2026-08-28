import { useEffect } from "react";
import { formatTime } from "../domain/dates";
import { useAppStore } from "../store/AppStore";

const NOTIFIED_KEY = "dayorder.notifications.v1";

function readNotified(): string[] {
  try {
    const value = JSON.parse(localStorage.getItem(NOTIFIED_KEY) ?? "[]");
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

export function ReminderManager() {
  const { data } = useAppStore();

  useEffect(() => {
    if (!data.settings.remindersEnabled || !("Notification" in window) || Notification.permission !== "granted") return;
    const check = () => {
      const now = Date.now();
      const notified = new Set(readNotified());
      let changed = false;
      data.events.forEach((event) => {
        const start = new Date(event.startAt).getTime();
        event.reminderMinutes.forEach((minutes) => {
          const key = `${event.id}:${event.startAt}:${minutes}`;
          const trigger = start - minutes * 60_000;
          if (notified.has(key) || trigger > now || now - trigger > 5 * 60_000 || start < now - 60_000) return;
          new Notification(event.title, { body: `${formatTime(event.startAt)} 开始${event.location ? ` · ${event.location}` : ""}`, tag: key });
          notified.add(key);
          changed = true;
        });
      });
      if (changed) localStorage.setItem(NOTIFIED_KEY, JSON.stringify([...notified].slice(-200)));
    };
    check();
    const timer = window.setInterval(check, 30_000);
    return () => window.clearInterval(timer);
  }, [data.events, data.settings.remindersEnabled]);

  return null;
}
