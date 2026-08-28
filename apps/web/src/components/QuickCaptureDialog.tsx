import { CalendarDays, CheckSquare2, Flag, NotebookPen, Sparkles, StickyNote } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { parseCapture } from "../domain/capture";
import type { EntityKind } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

type CaptureKind = EntityKind | "auto";

const kinds: Array<{ kind: CaptureKind; label: string; icon: typeof StickyNote }> = [
  { kind: "auto", label: "自动识别", icon: Sparkles },
  { kind: "record", label: "记录", icon: StickyNote },
  { kind: "task", label: "任务", icon: CheckSquare2 },
  { kind: "event", label: "日程", icon: CalendarDays },
  { kind: "note", label: "笔记", icon: NotebookPen },
  { kind: "goal", label: "目标", icon: Flag },
];

export function QuickCaptureDialog({ preferred, onClose }: { preferred: CaptureKind | null; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const [text, setText] = useState("");
  const [kind, setKind] = useState<CaptureKind>("auto");

  useEffect(() => {
    if (preferred) setKind(preferred === "review" ? "record" : preferred);
  }, [preferred]);

  const draft = useMemo(() => text.trim() ? parseCapture(text, data.goals, kind === "auto" ? undefined : kind) : null, [data.goals, kind, text]);

  const save = () => {
    if (!draft) return;
    dispatch({ type: "save-capture", draft });
    const label = kinds.find((item) => item.kind === draft.kind)?.label ?? "内容";
    setText("");
    onClose();
    toast(`${label}已保存，原始文本仍保留在记录中`);
  };

  return (
    <Modal
      open={preferred !== null}
      title="快速记录"
      description="先保留原话，再确认它应该去哪里。"
      onClose={onClose}
      footer={<><button className="button secondary" type="button" onClick={onClose}>取消</button><button className="button primary" type="button" onClick={save} disabled={!draft}>保留原文并创建</button></>}
    >
      <div className="capture-types" role="tablist" aria-label="记录类型">
        {kinds.map((item) => {
          const Icon = item.icon;
          return <button key={item.kind} className={`type-button ${kind === item.kind ? "active" : ""}`} type="button" role="tab" aria-selected={kind === item.kind} onClick={() => setKind(item.kind)}><Icon size={15} />{item.label}</button>;
        })}
      </div>
      <label className="field-label" htmlFor="capture-text">原始文本</label>
      <textarea id="capture-text" className="capture-area" data-autofocus value={text} onChange={(event) => setText(event.target.value)} placeholder="例如：周五下午 3 点看牙" />
      {draft ? (
        <div className="parse-preview-card">
          <span className="parse-icon"><Sparkles size={16} /></span>
          <div><strong>建议整理为：{kinds.find((item) => item.kind === draft.kind)?.label}</strong><p>{draft.explanation}</p>{draft.startAt && <small>时间：{new Date(draft.startAt).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</small>}</div>
          <span className="confidence">{Math.round(draft.confidence * 100)}%</span>
        </div>
      ) : <div className="parse-placeholder"><Sparkles size={15} />输入后会在本地生成结构化草稿</div>}
    </Modal>
  );
}
