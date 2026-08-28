import { ArrowLeft, CheckCircle2, KeyRound } from "lucide-react";
import { useState, type FormEvent } from "react";
import { ApiError } from "../api/http";
import { useAuth } from "../auth/AuthProvider";

export function PasswordResetPage() {
  const auth = useAuth();
  const token = new URLSearchParams(window.location.search).get("token")?.trim() ?? "";
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [pending, setPending] = useState(false);
  const [complete, setComplete] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError("");
    if (!token) { setError("重置链接缺少令牌，请重新申请密码重置邮件"); return; }
    if (password.length < 10) { setError("新密码需要至少 10 个字符"); return; }
    if (password !== confirmation) { setError("两次输入的密码不一致"); return; }
    setPending(true);
    try {
      await auth.completePasswordReset(token, password);
      setComplete(true);
    } catch (reason) {
      setError(reason instanceof ApiError || reason instanceof Error ? reason.message : "密码重置未完成，请重新申请链接");
    } finally {
      setPending(false);
    }
  };

  const backToLogin = () => {
    window.history.replaceState({}, "", "/");
    auth.openAuth("account", "login");
  };

  return <main className="auth-route-shell">
    <section className="auth-route-card" aria-labelledby="reset-title">
      <div className="auth-route-mark">{complete ? <CheckCircle2 size={24} /> : <KeyRound size={24} />}</div>
      <p className="eyebrow">账户安全</p>
      <h1 id="reset-title">{complete ? "密码已更新" : "设置新密码"}</h1>
      <p>{complete ? "旧会话已经失效。请用新密码重新登录。" : "使用 10–128 个字符。完成后，其他设备上的旧会话会失效。"}</p>
      {complete ? <button className="button primary" type="button" onClick={backToLogin}>返回登录</button> : <form className="auth-form" onSubmit={submit} noValidate>
        <label className="form-field"><span>新密码</span><input type="password" autoComplete="new-password" value={password} onChange={(event) => setPassword(event.target.value)} /></label>
        <label className="form-field"><span>再次输入</span><input type="password" autoComplete="new-password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label>
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="button primary auth-submit" type="submit" disabled={pending}>{pending ? "正在更新…" : "更新密码"}</button>
      </form>}
      {!complete && <button className="text-button auth-route-back" type="button" onClick={backToLogin}><ArrowLeft size={15} />返回登录</button>}
    </section>
  </main>;
}
