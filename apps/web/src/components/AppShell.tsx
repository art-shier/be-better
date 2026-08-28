import { Bot, CalendarDays, Cloud, CloudOff, Flag, HardDrive, Home, NotebookPen, Plus, RefreshCw, Search, Sparkles, StickyNote } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useAuth } from "../auth/AuthProvider";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { AccountControls } from "./AccountControls";
import { AssistantDrawer } from "./AssistantDrawer";
import { ReminderManager } from "./ReminderManager";

export type ViewName = "today" | "goals" | "calendar" | "records" | "notes" | "agent";

const nav = [
  { id: "today" as const, label: "今天", icon: Home },
  { id: "goals" as const, label: "目标", icon: Flag },
  { id: "calendar" as const, label: "日程", icon: CalendarDays },
  { id: "records" as const, label: "记录", icon: StickyNote },
  { id: "notes" as const, label: "笔记", icon: NotebookPen },
  { id: "agent" as const, label: "Agent", icon: Sparkles },
];

export function AppShell({ view, onNavigate, online, children }: { view: ViewName; onNavigate(view: ViewName): void; online: boolean; children: ReactNode }) {
  const { data, syncStatus, lastSyncedAt } = useAppStore();
  const { openCapture, openSearch, toast } = useUi();
  const auth = useAuth();
  const [assistantOpen, setAssistantOpen] = useState(false);
  const inboxCount = data.records.filter((record) => record.kind === "inbox" && !record.archivedAt && !record.parsedEntityId).length;

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") { event.preventDefault(); openSearch(); }
      if (event.key.toLowerCase() === "n" && !event.ctrlKey && !event.metaKey && !["INPUT", "TEXTAREA", "SELECT"].includes((document.activeElement as HTMLElement | null)?.tagName ?? "")) openCapture();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [openCapture, openSearch]);

  const badge = (id: ViewName) => id === "records" ? inboxCount : 0;
  const syncMessage = !auth.user
    ? "游客数据仅保存在当前浏览器"
    : !online
    ? "当前离线，所有更改会继续保存在本地"
    : syncStatus === "synced"
      ? "数据已同步到本地服务，断网时仍会保存在浏览器"
      : syncStatus === "connecting"
        ? "正在连接本地服务"
        : syncStatus === "conflict"
          ? "检测到版本冲突，本地版本已备份并载入服务端数据"
          : syncStatus === "local-only"
            ? "数据仅保存在当前浏览器"
            : "本地服务暂不可用，更改会在恢复连接后同步";
  const SyncIcon = !online || syncStatus === "offline" ? CloudOff : syncStatus === "connecting" ? RefreshCw : syncStatus === "synced" ? Cloud : HardDrive;
  const showOfflineBanner = Boolean(auth.user) && (!online || syncStatus === "offline" || auth.mode === "expired");
  const selectView = (next: ViewName) => {
    if (next === "agent" && !auth.canUseAgent) { auth.openAuth("agent"); return; }
    onNavigate(next);
  };

  return (
    <>
      <ReminderManager />
      <header className="app-header">
        <button className="brand" type="button" onClick={() => onNavigate("today")} aria-label="返回今天"><span className="brand-mark">序</span><span className="brand-copy"><strong>日序</strong><small>DayOrder</small></span></button>
        <nav className="primary-nav" aria-label="主导航">
          {nav.map((item) => <button key={item.id} className={`nav-button ${view === item.id ? "active" : ""}`} type="button" onClick={() => selectView(item.id)} aria-current={view === item.id ? "page" : undefined}>{item.label}{badge(item.id) > 0 && <span className="nav-count">{badge(item.id)}</span>}</button>)}
        </nav>
        <div className="header-tools">
          <button className="command-button" type="button" onClick={openSearch}><Search size={17} /><span>查找任何内容</span><kbd>Ctrl K</kbd></button>
          <button className="header-icon" type="button" onClick={() => toast(syncMessage)} aria-label={syncMessage}><SyncIcon className={syncStatus === "connecting" ? "sync-spinning" : undefined} size={18} /></button>
          <button className="header-icon assistant-trigger" type="button" onClick={() => auth.canUseAgent ? setAssistantOpen(true) : auth.openAuth("agent")} aria-label="打开 Agent 快捷面板"><Bot size={19} /></button>
          <button className="quick-button" type="button" onClick={() => openCapture()}><Plus size={18} /><span>快速记录</span></button>
          <AccountControls syncStatus={syncStatus} lastSyncedAt={lastSyncedAt} />
        </div>
      </header>
      {showOfflineBanner && <div className="offline-banner" role="status">{auth.mode === "expired" ? "登录已过期" : !online ? "当前离线" : "本地服务暂不可用"}：更改仍会保存在浏览器，恢复连接后继续同步。</div>}
      <main className="workspace">{children}</main>
      <nav className="bottom-nav" aria-label="移动端主导航">
        {nav.map((item) => { const Icon = item.icon; return <button key={item.id} className={`bottom-button ${view === item.id ? "active" : ""}`} type="button" onClick={() => selectView(item.id)} aria-current={view === item.id ? "page" : undefined}><Icon size={18} /><span>{item.label}</span>{badge(item.id) > 0 && <b>{badge(item.id)}</b>}</button>; })}
      </nav>
      <button className="mobile-capture" type="button" onClick={() => openCapture()} aria-label="快速记录"><Plus size={20} /></button>
      <AssistantDrawer open={assistantOpen && auth.canUseAgent} onClose={() => setAssistantOpen(false)} onOpenAgent={() => { setAssistantOpen(false); selectView("agent"); }} />
    </>
  );
}
