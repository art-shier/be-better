import { AlertTriangle, Check, MailCheck, RefreshCw } from "lucide-react";
import { useState } from "react";
import { useAuth } from "../auth/AuthProvider";

export function VerificationNotice() {
  const auth = useAuth();
  const [resending, setResending] = useState(false);
  const [resendState, setResendState] = useState<"idle" | "sent" | "error">("idle");
  const pending = auth.pendingVerification;
  if (!pending && !auth.verificationBusy && !auth.verificationError && !auth.migrationError) return null;

  const resend = async () => {
    setResending(true);
    setResendState("idle");
    try {
      await auth.resendVerification();
      setResendState("sent");
    } catch {
      setResendState("error");
    } finally {
      setResending(false);
    }
  };

  if (auth.migrationError) {
    return <aside className="verification-ribbon migration-warning" role="status">
      <span className="verification-ribbon-icon"><AlertTriangle size={18} /></span>
      <div><strong>邮箱已验证，本机数据仍在等待同步</strong><p>{auth.migrationError}。游客副本尚未删除，可以稍后重试。</p></div>
    </aside>;
  }

  return <aside className="verification-ribbon" aria-live="polite">
    <span className="verification-ribbon-icon">{auth.verificationBusy ? <RefreshCw className="spin" size={18} /> : <MailCheck size={18} />}</span>
    <div>
      <strong>{auth.verificationBusy ? "正在验证邮箱" : auth.verificationError ? "验证链接未生效" : "验证邮件已发送"}</strong>
      <p>{auth.verificationError ?? (pending ? `请打开发送至 ${pending.user.email} 的邮件。验证后才会创建登录会话。` : "正在确认验证链接。")}</p>
    </div>
    {pending && !auth.verificationBusy && <button className="button secondary small" type="button" onClick={() => void resend()} disabled={resending}>
      {resendState === "sent" ? <><Check size={15} />已重新发送</> : <><RefreshCw size={15} />{resending ? "发送中…" : "重新发送"}</>}
    </button>}
    {resendState === "error" && <small role="alert">发送失败，请稍后重试</small>}
  </aside>;
}
