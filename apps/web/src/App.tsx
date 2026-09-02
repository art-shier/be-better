import { useEffect, useState } from "react";
import { useAuth } from "./auth/AuthProvider";
import { AppShell, type ViewName } from "./components/AppShell";
import { AuthDialog } from "./components/AuthDialog";
import { PasswordResetPage } from "./components/PasswordResetPage";
import { VerificationNotice } from "./components/VerificationNotice";
import { Bot, LockKeyhole } from "lucide-react";
import { AgentPage } from "./pages/AgentPage";
import { CalendarPage } from "./pages/CalendarPage";
import { GoalsPage } from "./pages/GoalsPage";
import { NotesPage } from "./pages/NotesPage";
import { RecordsPage } from "./pages/RecordsPage";
import { TodayPage } from "./pages/TodayPage";

const views: ViewName[] = ["today", "goals", "calendar", "records", "notes", "agent"];
const viewFromHash = (agentAvailable: boolean): ViewName => {
  const value = window.location.hash.slice(1) as ViewName;
  return views.includes(value) && (agentAvailable || value !== "agent") ? value : "today";
};

export default function App({ agentAvailable = false }: { agentAvailable?: boolean }) {
  const auth = useAuth();
  const [view, setView] = useState<ViewName>(() => viewFromHash(agentAvailable));
  const [online, setOnline] = useState(navigator.onLine);

  useEffect(() => {
    const onHash = () => {
      const next = viewFromHash(agentAvailable);
      if (!agentAvailable && window.location.hash === "#agent") window.history.replaceState(window.history.state, "", `#${next}`);
      setView(next);
    };
    const onOnline = () => setOnline(true);
    const onOffline = () => setOnline(false);
    onHash();
    window.addEventListener("hashchange", onHash);
    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    return () => { window.removeEventListener("hashchange", onHash); window.removeEventListener("online", onOnline); window.removeEventListener("offline", onOffline); };
  }, [agentAvailable]);

  const navigate = (next: ViewName) => {
    if (next === "agent" && !agentAvailable) return;
    if (next === "agent" && !auth.canUseAgent) { auth.openAuth("agent"); return; }
    window.location.hash = next;
    setView(next);
    window.scrollTo({ top: 0, behavior: "smooth" });
  };

  if (window.location.pathname === "/reset-password") {
    return <><PasswordResetPage /><AuthDialog /></>;
  }

  return (
    <>
      <VerificationNotice />
      <AppShell view={view} onNavigate={navigate} online={online} agentAvailable={agentAvailable}>
        {view === "today" && <TodayPage />}
        {view === "goals" && <GoalsPage />}
        {view === "calendar" && <CalendarPage />}
        {view === "records" && <RecordsPage />}
        {view === "notes" && <NotesPage />}
        {agentAvailable && view === "agent" && (auth.canUseAgent ? <AgentPage /> : <AgentGate onLogin={() => auth.openAuth("agent")} />)}
      </AppShell>
      <AuthDialog />
    </>
  );
}

function AgentGate({ onLogin }: { onLogin(): void }) {
  return <section className="agent-gate"><span><LockKeyhole size={19} /></span><p className="eyebrow">身份与权限</p><h1>Agent 需要验证账户</h1><p>Agent 会在你授权后读取账户数据。今天、目标、日程、记录和笔记仍可作为游客保存在本机。</p><button className="button primary" type="button" onClick={onLogin}><Bot size={17} />登录后使用 Agent</button></section>;
}
