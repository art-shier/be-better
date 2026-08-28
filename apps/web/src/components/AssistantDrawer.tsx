import { ArrowRight, Lock, Send, Sparkles, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useAppStore } from "../store/AppStore";

interface Message { id: string; role: "user" | "assistant"; text: string }

export function AssistantDrawer({ open, onClose, onOpenAgent }: { open: boolean; onClose(): void; onOpenAgent(): void }) {
  const { data } = useAppStore();
  const [messages, setMessages] = useState<Message[]>([{ id: "welcome", role: "assistant", text: "我可以根据已授权的目标、日程和记录给出建议。需要修改数据时，我只会生成待确认变更。" }]);
  const [input, setInput] = useState("");
  const drawerRef = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const frame = window.requestAnimationFrame(() => drawerRef.current?.querySelector<HTMLTextAreaElement>("textarea")?.focus());
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKeyDown);
    return () => { window.cancelAnimationFrame(frame); document.removeEventListener("keydown", onKeyDown); previous?.focus(); };
  }, [onClose, open]);

  const send = (text = input) => {
    const content = text.trim();
    if (!content) return;
    setMessages((items) => [...items, { id: `${Date.now()}_u`, role: "user", text: content }]);
    setInput("");
    window.setTimeout(() => {
      const nextEvent = [...data.events].sort((a, b) => a.startAt.localeCompare(b.startAt)).find((event) => new Date(event.startAt) > new Date());
      const activeGoal = data.goals.find((goal) => goal.status === "active" && goal.health !== "normal") ?? data.goals.find((goal) => goal.status === "active");
      const answer = content.includes("下午")
        ? `下午${nextEvent ? `有“${nextEvent.title}”` : "没有固定日程"}。建议先留 25 分钟整理轻量事项，再给固定日程预留缓冲。依据来自今日日程和当前精力 ${data.settings.energy}/5。`
        : `建议先推进“${activeGoal?.title ?? "最重要的目标"}”相关任务。我查看了授权范围内的目标、今日任务和日程；这是建议，没有写入任何数据。`;
      setMessages((items) => [...items, { id: `${Date.now()}_a`, role: "assistant", text: answer }]);
    }, 420);
  };

  return (
    <aside ref={drawerRef} className={`assistant-drawer ${open ? "open" : ""}`} aria-hidden={!open} inert={!open} aria-label="Agent 快捷面板">
      <div className="drawer-head"><div><span><Sparkles size={15} />Agent 快捷面板</span><p>只读回答；管理任务转到工作台</p></div><button type="button" onClick={onClose} aria-label="关闭 Agent 面板"><X size={18} /></button></div>
      <div className="drawer-scope"><Lock size={14} />本次可读取：目标、任务、今日日程</div>
      <div className="assistant-messages" aria-live="polite">
        {messages.map((message) => <div key={message.id} className={`message ${message.role}`}>{message.text}</div>)}
        {messages.length === 1 && <div className="suggestions"><button type="button" onClick={() => send("下午怎么安排更合理？")}>下午怎么安排更合理？</button><button type="button" onClick={() => send("我现在应该先做什么？")}>我现在应该先做什么？</button></div>}
      </div>
      <button className="open-workspace" type="button" onClick={onOpenAgent}>打开 Agent 工作台 <ArrowRight size={15} /></button>
      <div className="assistant-compose"><textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); send(); } }} placeholder="问一个与今天有关的问题" /><button type="button" onClick={() => send()} aria-label="发送"><Send size={17} /></button></div>
    </aside>
  );
}
