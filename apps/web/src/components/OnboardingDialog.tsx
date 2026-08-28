import { ArrowLeft, ArrowRight, Check, Plus, ShieldCheck, Sparkles, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { createId } from "../domain/ids";
import { useAuth } from "../auth/AuthProvider";
import type { Area, DataMode, Goal } from "../domain/types";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

interface GoalDraft { id: string; title: string; why: string; area: Area; dueDate: string }
const areas: Area[] = ["健康", "成长", "工作", "关系", "财务", "生活"];
const stepLabels = ["关注领域", "近期目标", "数据模式", "今日行动"];
const newGoal = (area: Area): GoalDraft => ({ id: createId("goal-draft"), title: "", why: "", area, dueDate: "" });

export function OnboardingDialog() {
  const auth = useAuth();
  const { data, dispatch, syncStatus } = useAppStore();
  const { toast } = useUi();
  const [step, setStep] = useState(0);
  const [focusAreas, setFocusAreas] = useState<Area[]>(["工作"]);
  const [goals, setGoals] = useState<GoalDraft[]>([newGoal("工作")]);
  const [dataMode, setDataMode] = useState<DataMode>("local");
  const open = !data.settings.onboardingCompleted && !(auth.mode === "authenticated" && syncStatus === "connecting");
  const goalsValid = goals.length > 0 && goals.every((goal) => goal.title.trim() && goal.why.trim());
  const canContinue = step === 0 ? focusAreas.length > 0 : step === 1 ? goalsValid : true;
  const primaryGoal = goals.find((goal) => goal.title.trim());
  const progress = useMemo(() => `${step + 1} / ${stepLabels.length}`, [step]);

  const toggleArea = (area: Area) => {
    setFocusAreas((current) => current.includes(area) ? current.filter((item) => item !== area) : [...current, area]);
    if (!focusAreas.includes(area) && goals.length === 1 && !goals[0].title) setGoals([{ ...goals[0], area }]);
  };
  const updateGoal = (id: string, patch: Partial<GoalDraft>) => setGoals((current) => current.map((goal) => goal.id === id ? { ...goal, ...patch } : goal));
  const finish = () => {
    if (!goalsValid) return;
    const now = new Date().toISOString();
    const created: Goal[] = goals.slice(0, 3).map((goal) => ({ id: createId("goal"), title: goal.title.trim(), why: goal.why.trim(), area: goal.area, metricType: "project", targetValue: 100, currentValue: 0, unit: "%", startAt: now, dueAt: goal.dueDate ? new Date(`${goal.dueDate}T23:59:00`).toISOString() : undefined, status: "active", health: "normal", milestones: [], createdAt: now, updatedAt: now }));
    dispatch({ type: "complete-onboarding", goals: created, focusAreas, dataMode });
    toast("首次设置已完成，已生成第一个今日行动");
  };

  const footer = <><span className="onboarding-progress">{progress} · {stepLabels[step]}</span>{step > 0 && <button className="button secondary" type="button" onClick={() => setStep((value) => value - 1)}><ArrowLeft size={16} />上一步</button>}<button className="button primary" type="button" disabled={!canContinue} onClick={() => step === 3 ? finish() : setStep((value) => value + 1)}>{step === 3 ? <><Check size={16} />开始使用</> : <>下一步<ArrowRight size={16} /></>}</button></>;

  return <Modal open={open} title="把接下来想发生的变化放进日序" description="约 3 分钟；所有设置之后都可以调整。" onClose={() => undefined} dismissible={false} size="large" footer={footer}>
    <div className="onboarding-steps" aria-label="设置进度">{stepLabels.map((label, index) => <span key={label} className={index <= step ? "active" : ""}><b>{index + 1}</b>{label}</span>)}</div>
    {step === 0 && <section className="onboarding-section"><h3>最近最想照顾哪些部分？</h3><p>选择 1–3 个领域，首页会优先展示相关目标。</p><div className="onboarding-areas">{areas.map((area) => <button key={area} className={focusAreas.includes(area) ? "active" : ""} type="button" aria-pressed={focusAreas.includes(area)} onClick={() => toggleArea(area)}>{focusAreas.includes(area) && <Check size={15} />}{area}</button>)}</div></section>}
    {step === 1 && <section className="onboarding-section"><h3>创建 1–3 个近期目标</h3><p>先写清楚变化和原因，衡量方式可以稍后细化。</p><div className="onboarding-goals">{goals.map((goal, index) => <fieldset key={goal.id}><legend>目标 {index + 1}</legend><label className="form-field"><span>目标名称</span><input data-autofocus={index === 0 || undefined} value={goal.title} onChange={(event) => updateGoal(goal.id, { title: event.target.value })} placeholder="例如：稳定跑完 10 公里" /></label><label className="form-field"><span>为什么重要</span><textarea value={goal.why} onChange={(event) => updateGoal(goal.id, { why: event.target.value })} placeholder="这个变化会带来什么？" /></label><div className="onboarding-goal-row"><label className="form-field"><span>领域</span><select value={goal.area} onChange={(event) => updateGoal(goal.id, { area: event.target.value as Area })}>{areas.map((area) => <option key={area}>{area}</option>)}</select></label><label className="form-field"><span>期望日期（可选）</span><input type="date" value={goal.dueDate} onChange={(event) => updateGoal(goal.id, { dueDate: event.target.value })} /></label></div>{goals.length > 1 && <button className="text-button danger-text" type="button" onClick={() => setGoals((current) => current.filter((item) => item.id !== goal.id))}><Trash2 size={14} />移除</button>}</fieldset>)}</div>{goals.length < 3 && <button className="button secondary add-onboarding-goal" type="button" onClick={() => setGoals((current) => [...current, newGoal(focusAreas[current.length] ?? focusAreas[0] ?? "生活")])}><Plus size={16} />再加一个目标</button>}</section>}
    {step === 2 && <section className="onboarding-section"><h3>选择数据与 AI 模式</h3><p>无论选择哪种模式，写入数据前都需要你确认。</p><div className="onboarding-modes"><button className={dataMode === "local" ? "active" : ""} type="button" onClick={() => setDataMode("local")}><ShieldCheck size={20} /><span><strong>完全本地</strong><small>数据只保存在当前浏览器；规则功能照常使用。</small></span></button><button className={dataMode === "selected" ? "active" : ""} type="button" onClick={() => setDataMode("selected")}><Sparkles size={20} /><span><strong>仅发送选中内容</strong><small>使用 AI 时先展示上下文清单，再由你确认。</small></span></button></div><div className="onboarding-import"><strong>日历与 Markdown 导入</strong><span>这是可选步骤，可在“数据与设置”中随时导入备份或添加日程。</span></div></section>}
    {step === 3 && <section className="onboarding-section"><h3>第一个今日行动已经准备好</h3><p>确认后只写入一个 45 分钟行动，不会自动改动其他日程。</p><div className="first-plan"><span><Sparkles size={18} /></span><div><strong>{primaryGoal ? `推进：${primaryGoal.title}` : "推进第一个目标"}</strong><p>从一个 45 分钟、可以立即验证的最小动作开始。</p><small>依据：你的首要目标 · 当前可用时间 · 精力默认 3 / 5</small></div><b>88%</b></div><div className="context-list"><strong>本次将写入</strong><span>{goals.length} 个目标</span><span>1 个今日任务</span><span>0 项日程改动</span></div></section>}
  </Modal>;
}
