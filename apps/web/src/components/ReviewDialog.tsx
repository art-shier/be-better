import { Sparkles } from "lucide-react";
import { useMemo, useState } from "react";
import { dateKey } from "../domain/dates";
import { createId } from "../domain/ids";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

export function ReviewDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const { data, dispatch } = useAppStore();
  const { toast } = useUi();
  const existing = data.reviews.find((review) => review.date === dateKey(new Date()));
  const [wins, setWins] = useState(existing?.wins ?? "");
  const [blockers, setBlockers] = useState(existing?.blockers ?? "");
  const [tomorrowFocus, setTomorrowFocus] = useState(existing?.tomorrowFocus ?? "");
  const [mood, setMood] = useState(existing?.mood ?? 3);
  const completed = useMemo(() => data.tasks.filter((task) => task.status === "done" && task.completedAt && dateKey(task.completedAt) === dateKey(new Date())), [data.tasks]);

  const save = () => {
    dispatch({ type: "save-review", review: { id: existing?.id ?? createId("review"), date: dateKey(new Date()), wins, blockers, tomorrowFocus, mood, energy: data.settings.energy, aiSummary: `事实：今天完成 ${completed.length} 项任务。建议：明天先推进“${tomorrowFocus || "最重要的一件事"}”。` } });
    onClose();
    toast("今日复盘已保存");
  };

  return (
    <Modal open={open} title="两分钟晚间复盘" description="事实和推断分开记录，给明天留下一个清楚起点。" onClose={onClose} footer={<><button className="button secondary" type="button" onClick={onClose}>稍后再写</button><button className="button primary" type="button" onClick={save}>保存复盘</button></>}>
      <div className="review-fact"><Sparkles size={16} /><span><strong>今天已经完成 {completed.length} 项</strong><small>{completed.map((task) => task.title).join("、") || "完成任务后会在这里形成事实摘要"}</small></span></div>
      <label className="form-field"><span>完成了什么</span><textarea value={wins} onChange={(event) => setWins(event.target.value)} placeholder="今天值得保留的进展" /></label>
      <label className="form-field"><span>什么被阻塞</span><textarea value={blockers} onChange={(event) => setBlockers(event.target.value)} placeholder="没有也可以留空" /></label>
      <label className="form-field"><span>明天最重要的一件事</span><input value={tomorrowFocus} onChange={(event) => setTomorrowFocus(event.target.value)} placeholder="只写一件" /></label>
      <fieldset className="score-field"><legend>今天整体状态</legend><div>{[1,2,3,4,5].map((value) => <button key={value} className={mood === value ? "active" : ""} type="button" onClick={() => setMood(value)}>{value}</button>)}</div></fieldset>
    </Modal>
  );
}
