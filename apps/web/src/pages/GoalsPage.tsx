import { BookOpen, BriefcaseBusiness, Camera, Check, Flag, HeartPulse, Pause, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import type { Goal, GoalStatus } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";

const iconFor = (goal: Goal) => goal.area === "健康" ? HeartPulse : goal.area === "工作" ? BriefcaseBusiness : goal.title.includes("阅读") ? BookOpen : goal.title.includes("摄影") ? Camera : Flag;
const statusLabel: Record<GoalStatus, string> = { active: "进行中", paused: "已暂停", completed: "已完成", abandoned: "已放弃" };

export function GoalsPage() {
  const { data, dispatch } = useAppStore();
  const { editGoal, openReview, toast } = useUi();
  const [filter, setFilter] = useState<GoalStatus>("active");
  const visible = useMemo(() => data.goals.filter((goal) => goal.status === filter), [data.goals, filter]);
  const feature = visible[0];

  const progress = (goal: Goal) => Math.min(100, Math.round(goal.currentValue / Math.max(1, goal.targetValue) * 100));
  const recordProgress = (goal: Goal) => {
    const next = Math.min(goal.targetValue, goal.currentValue + 1);
    dispatch({ type: "update-goal", goal: { ...goal, currentValue: next, health: "normal", updatedAt: new Date().toISOString() } });
    toast(`“${goal.title}”进度已更新`);
  };
  const toggleMilestone = (goal: Goal, milestoneId: string) => {
    const milestones = goal.milestones.map((item) => item.id === milestoneId ? { ...item, completedAt: item.completedAt ? undefined : new Date().toISOString() } : item);
    dispatch({ type: "update-goal", goal: { ...goal, milestones, currentValue: milestones.filter((item) => item.completedAt).length, updatedAt: new Date().toISOString() } });
  };

  return <div className="view-page"><header className="view-head"><div><p className="eyebrow">方向与进展</p><h1>目标不是数字，是接下来要发生的变化。</h1><p>用里程碑、累计、习惯或项目完成度衡量，不强迫所有目标变成百分比。</p></div><div className="head-actions"><button className="button secondary" type="button" onClick={openReview}>今日复盘</button><button className="button primary" type="button" onClick={() => editGoal()}><Plus size={17} />新建目标</button></div></header>
    <div className="filter-tabs" role="tablist" aria-label="目标状态">{(["active","paused","completed","abandoned"] as GoalStatus[]).map((status) => <button key={status} className={filter === status ? "active" : ""} type="button" role="tab" aria-selected={filter === status} onClick={() => setFilter(status)}>{statusLabel[status]} <span>{data.goals.filter((goal) => goal.status === status).length}</span></button>)}</div>
    {!visible.length ? <div className="empty-state page-empty"><Flag size={28} /><strong>这里还没有{statusLabel[filter]}的目标</strong><p>{filter === "active" ? "创建一个近期目标，先说明为什么重要。" : "状态变化后会保留在这里。"}</p>{filter === "active" && <button className="button primary" type="button" onClick={() => editGoal()}>创建目标</button>}</div> : <div className="goal-layout">
      {feature && <article className="goal-feature" data-entity-id={feature.id} tabIndex={-1}><div className="goal-feature-top"><span>{feature.area} · {feature.metricType === "milestone" ? "里程碑" : feature.metricType === "habit" ? "周期习惯" : "项目进展"}</span><span className={`health ${feature.health}`}>{feature.health === "normal" ? "进展正常" : feature.health === "attention" ? "需要关注" : "已经停滞"}</span></div><button className="goal-feature-copy" type="button" onClick={() => editGoal(feature)}><h2>{feature.title}</h2><p>{feature.why}</p></button>{feature.milestones.length ? <div className="milestone-list">{feature.milestones.map((item) => <label key={item.id}><button type="button" onClick={() => toggleMilestone(feature, item.id)}>{item.completedAt ? <Check size={14} /> : <span />}</button><span className={item.completedAt ? "done" : ""}>{item.title}</span></label>)}</div> : <div className="feature-progress"><span>{feature.currentValue} / {feature.targetValue} {feature.unit}</span><strong>{progress(feature)}%</strong><div><i style={{ width: `${progress(feature)}%` }} /></div></div>}</article>}
      {visible.slice(1).map((goal) => { const Icon = iconFor(goal); return <article className="panel goal-card" key={goal.id} data-entity-id={goal.id} tabIndex={-1}><div className="goal-card-top"><span className="goal-icon"><Icon size={20} /></span><span className={`health ${goal.health}`}>{goal.health === "normal" ? "进展正常" : goal.health === "attention" ? "需要关注" : "已经停滞"}</span></div><button className="goal-card-copy" type="button" onClick={() => editGoal(goal)}><h2>{goal.title}</h2><p>{goal.why}</p></button><div className="goal-stat"><span>{goal.metricType === "habit" ? "本周期" : "当前进度"}</span><strong>{goal.currentValue} / {goal.targetValue} {goal.unit}</strong></div><div className="progress"><i style={{ width: `${progress(goal)}%` }} /></div><div className="goal-card-actions"><button className="text-button" type="button" onClick={() => recordProgress(goal)}><Plus size={14} />记录进展</button><button className="icon-text" type="button" onClick={() => dispatch({ type: "update-goal", goal: { ...goal, status: goal.status === "paused" ? "active" : "paused", updatedAt: new Date().toISOString() } })}>{goal.status === "paused" ? <Flag size={14} /> : <Pause size={14} />}{goal.status === "paused" ? "继续" : "暂停"}</button></div></article>; })}
    </div>}
  </div>;
}
