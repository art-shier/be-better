import { ArrowRight, Lock, Send, Sparkles, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { createAgentRun, getAgentRun, type AgentScope, type ServerAgentRun } from "../api/agent";
import type { AppData } from "../domain/types";
import { useAppStore } from "../store/AppStore";

interface Message { id: string; role: "user" | "assistant"; text: string; pending?: boolean }
type Permissions = AppData["settings"]["permissions"];

function readScope(permissions: Permissions): AgentScope {
  const domains: AgentScope["domains"] = [];
  if (permissions.goals) domains.push("goals", "tasks");
  if (permissions.calendar) domains.push("calendar");
  if (permissions.records) domains.push("records");
  if (permissions.privateNotes) domains.push("notes");
  const from = new Date();
  const to = new Date();
  from.setDate(from.getDate() - (permissions.records ? 14 : 0));
  to.setDate(to.getDate() + (permissions.calendar ? 30 : 0));
  return {
    domains,
    entityIds: [],
    ...(permissions.records || permissions.calendar ? { from: from.toISOString(), to: to.toISOString() } : {}),
  };
}

function wait(delay: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, delay));
}

async function waitForResult(initial: ServerAgentRun): Promise<ServerAgentRun> {
  let run = initial;
  for (let attempt = 0; attempt < 30; attempt += 1) {
    if (["completed", "failed", "stopped", "waiting"].includes(run.status)) return run;
    await wait(1_000);
    run = await getAgentRun(run.id);
  }
  throw new Error("Agent 仍在后台分析，可到工作台继续查看");
}

function resultText(run: ServerAgentRun): string {
  if (run.status === "failed") return run.errorMessage ?? "Agent 分析失败，请稍后重试。";
  if (run.status === "stopped") return "这次只读分析已停止。";
  const sources = run.sourceRefs.map((item) => item.labelSnapshot).slice(0, 4);
  return `${run.summary ?? "只读分析已完成，没有修改数据。"}${sources.length ? ` 依据：${sources.join("、")}。` : ""}`;
}

export function AssistantDrawer({ open, onClose, onOpenAgent }: { open: boolean; onClose(): void; onOpenAgent(): void }) {
  const { data, createServerMutationContext } = useAppStore();
  const [messages, setMessages] = useState<Message[]>([{ id: "welcome", role: "assistant", text: "我可以根据已授权的目标、日程和记录给出建议。需要修改数据时，请转到 Agent 工作台逐项确认。" }]);
  const [input, setInput] = useState("");
  const [sending, setSending] = useState(false);
  const drawerRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const frame = window.requestAnimationFrame(() => drawerRef.current?.querySelector<HTMLTextAreaElement>("textarea")?.focus());
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKeyDown);
    return () => { window.cancelAnimationFrame(frame); document.removeEventListener("keydown", onKeyDown); previous?.focus(); };
  }, [onClose, open]);

  const send = async (text = input) => {
    const content = text.trim();
    if (!content || sending) return;
    const scope = readScope(data.settings.permissions);
    const answerId = crypto.randomUUID();
    setMessages((items) => [...items, { id: crypto.randomUUID(), role: "user", text: content }, { id: answerId, role: "assistant", text: "正在服务端读取授权数据…", pending: true }]);
    setInput("");
    setSending(true);
    try {
      if (!scope.domains.length) throw new Error("请先在设置中授权至少一个数据范围。");
      const run = await createAgentRun({ intent: content, actionMode: "read", scope }, await createServerMutationContext());
      const completed = await waitForResult(run);
      setMessages((items) => items.map((item) => item.id === answerId ? { ...item, text: resultText(completed), pending: false } : item));
    } catch (error) {
      const message = error instanceof Error ? error.message : "Agent 暂时无法回答，请稍后重试。";
      setMessages((items) => items.map((item) => item.id === answerId ? { ...item, text: message, pending: false } : item));
    } finally {
      setSending(false);
    }
  };

  const scopeLabel = [data.settings.permissions.goals && "目标与任务", data.settings.permissions.calendar && "日程", data.settings.permissions.records && "记录", data.settings.permissions.privateNotes && "笔记"].filter(Boolean).join("、") || "尚未授权";

  return (
    <aside ref={drawerRef} className={`assistant-drawer ${open ? "open" : ""}`} aria-hidden={!open} inert={!open} aria-label="Agent 快捷面板">
      <div className="drawer-head"><div><span><Sparkles size={15} />Agent 快捷面板</span><p>服务端只读回答；管理任务转到工作台</p></div><button type="button" onClick={onClose} aria-label="关闭 Agent 面板"><X size={18} /></button></div>
      <div className="drawer-scope"><Lock size={14} />本次可读取：{scopeLabel}</div>
      <div className="assistant-messages" aria-live="polite" aria-busy={sending}>
        {messages.map((message) => <div key={message.id} className={`message ${message.role} ${message.pending ? "pending" : ""}`}>{message.text}</div>)}
        {messages.length === 1 && <div className="suggestions"><button type="button" onClick={() => void send("下午怎么安排更合理？")}>下午怎么安排更合理？</button><button type="button" onClick={() => void send("我现在应该先做什么？")}>我现在应该先做什么？</button></div>}
      </div>
      <button className="open-workspace" type="button" onClick={onOpenAgent}>打开 Agent 工作台 <ArrowRight size={15} /></button>
      <div className="assistant-compose"><textarea value={input} disabled={sending} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); void send(); } }} placeholder="问一个与今天有关的问题" /><button type="button" disabled={sending || !input.trim()} onClick={() => void send()} aria-label="发送"><Send size={17} /></button></div>
    </aside>
  );
}
