import { AlertTriangle, MailCheck, RefreshCw } from "lucide-react";
import { useAuth } from "../auth/AuthProvider";

export function VerificationNotice() {
  const auth = useAuth();
  const pending = auth.pendingVerification;
  if (!pending && !auth.verificationBusy && !auth.verificationError && !auth.migrationError) return null;

  if (auth.migrationError) {
    return <aside className="verification-ribbon migration-warning" role="status">
      <span className="verification-ribbon-icon"><AlertTriangle size={18} /></span>
      <div><strong>账户已登录，本机数据仍在等待同步</strong><p>{auth.migrationError}。游客副本尚未删除，可以稍后重试。</p></div>
    </aside>;
  }

  if (pending && !auth.verificationBusy && !auth.verificationError) {
    return <aside className="verification-ribbon" role="status">
      <span className="verification-ribbon-icon"><MailCheck size={18} /></span>
      <div>
        <strong>账户已创建，请使用原密码登录</strong>
        <p>邮箱验证已停用。请使用 {pending.user.email} 和注册时设置的密码登录，登录后会继续迁移这台设备的数据。</p>
      </div>
    </aside>;
  }

  return <aside className="verification-ribbon" aria-live="polite">
    <span className="verification-ribbon-icon">{auth.verificationBusy ? <RefreshCw className="spin" size={18} /> : <MailCheck size={18} />}</span>
    <div>
      <strong>{auth.verificationBusy ? "正在确认旧验证链接" : "验证链接未生效"}</strong>
      <p>{auth.verificationError ?? "正在确认验证链接。"}</p>
    </div>
  </aside>;
}
