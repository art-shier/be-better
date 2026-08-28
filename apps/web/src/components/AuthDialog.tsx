import { Check, HardDrive, LockKeyhole, ShieldCheck } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthProvider";
import { ApiError } from "../api/client";
import { useAppStore } from "../store/AppStore";
import { Modal } from "./Modal";

export function AuthDialog() {
  const auth = useAuth();
  const { data } = useAppStore();
  const [tab, setTab] = useState<"login" | "register">(auth.dialog.initialTab);
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [migrate, setMigrate] = useState(true);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const emailRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!auth.dialog.open) return;
    setTab(auth.dialog.initialTab); setError(""); setPassword("");
  }, [auth.dialog.initialTab, auth.dialog.open]);

  const submit = async (event: FormEvent) => {
    event.preventDefault(); setError("");
    if (!email.trim()) { setError("请输入邮箱"); emailRef.current?.focus(); return; }
    if (password.length < 10) { setError("密码需要至少 10 个字符"); return; }
    if (tab === "register" && !displayName.trim()) { setError("请输入希望显示的称呼"); return; }
    setPending(true);
    try {
      if (tab === "login") await auth.login(email, password);
      else await auth.register({ displayName, email, password, migrate, data });
      setPassword("");
    } catch (reason) {
      setError(reason instanceof ApiError || reason instanceof Error ? reason.message : "暂时无法完成请求，请重试");
    } finally { setPending(false); }
  };

  const description = auth.dialog.reason === "agent"
    ? "Agent 会读取账户内的授权数据，因此需要先验证身份并保持在线。"
    : auth.dialog.reason === "expired"
      ? "登录已过期，本机更改仍在此设备。重新登录同一账户即可继续同步。"
      : "登录后可同步数据；不登录也能继续使用今天、目标、日程、记录和笔记。";

  return (
    <Modal open={auth.dialog.open} onClose={auth.closeAuth} title={auth.dialog.reason === "agent" ? "登录后使用 Agent" : auth.dialog.reason === "expired" ? "重新验证账户" : "账户与同步"} description={description} size="small">
      <div className="auth-tabs" role="tablist" aria-label="账户操作">
        <button type="button" role="tab" aria-selected={tab === "login"} className={tab === "login" ? "active" : ""} onClick={() => { setTab("login"); setError(""); }}>登录</button>
        <button type="button" role="tab" aria-selected={tab === "register"} className={tab === "register" ? "active" : ""} onClick={() => { setTab("register"); setError(""); }}>注册</button>
      </div>
      <form className="auth-form" onSubmit={submit} noValidate>
        {tab === "register" && <label className="form-field"><span>称呼</span><input value={displayName} onChange={(event) => setDisplayName(event.target.value)} autoComplete="name" maxLength={40} placeholder="例如：一山" /></label>}
        <label className="form-field"><span>邮箱</span><input ref={emailRef} value={email} onChange={(event) => setEmail(event.target.value)} type="email" autoComplete="email" placeholder="name@example.com" /></label>
        <label className="form-field"><span>密码</span><input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete={tab === "login" ? "current-password" : "new-password"} placeholder="至少 10 个字符" /></label>
        {tab === "register" && <label className={`migration-choice ${migrate ? "active" : ""}`}><input type="checkbox" checked={migrate} onChange={(event) => setMigrate(event.target.checked)} /><span className="migration-check">{migrate && <Check size={13} />}</span><span><strong>迁移这台设备的数据</strong><small>{data.goals.length} 个目标 · {data.tasks.length} 个任务 · {data.notes.length} 篇笔记</small></span></label>}
        {tab === "register" && <div className="auth-note"><HardDrive size={16} /><span>只有账户创建成功后才会清空游客空间；失败时本机数据不会变化。</span></div>}
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="button primary auth-submit" type="submit" disabled={pending}>{pending ? "正在处理…" : tab === "login" ? "登录账户" : "创建账户"}</button>
      </form>
      <div className="auth-assurance"><span><LockKeyhole size={14} />密码使用 Argon2id 保护</span><span><ShieldCheck size={14} />会话保存在安全 Cookie</span></div>
    </Modal>
  );
}
