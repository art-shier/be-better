import { ChevronRight, Cloud, CloudOff, HardDrive, LogOut, Settings, Shield, UserRound } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthProvider";
import { ApiError } from "../api/http";
import type { SyncStatus } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

export function AccountControls({ syncStatus, lastSyncedAt }: { syncStatus: SyncStatus; lastSyncedAt: string | null }) {
  const auth = useAuth();
  const { openSettings, toast } = useUi();
  const [menuOpen, setMenuOpen] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);
  const anchorRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const close = (event: MouseEvent) => { if (!anchorRef.current?.contains(event.target as Node)) setMenuOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") setMenuOpen(false); };
    document.addEventListener("mousedown", close); document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("mousedown", close); document.removeEventListener("keydown", escape); };
  }, [menuOpen]);

  if (!auth.user) return <button className="avatar-button guest-avatar" type="button" onClick={() => auth.openAuth("account")} aria-label="登录或注册">游</button>;
  const initial = [...auth.user.displayName][0]?.toUpperCase() ?? "序";
  const state = accountState(auth.mode, syncStatus, lastSyncedAt);
  const StateIcon = state.icon;

  return <div className="account-anchor" ref={anchorRef}>
    <button className="avatar-button" type="button" onClick={() => setMenuOpen((value) => !value)} aria-label="打开账户菜单" aria-expanded={menuOpen}>{initial}</button>
    {menuOpen && <div className="account-menu" role="menu">
      <div className="account-menu-head"><span>{initial}</span><div><strong>{auth.user.displayName}</strong><small>{auth.user.email}</small></div></div>
      <div className={`identity-band ${state.tone}`}><StateIcon size={15} /><span><strong>{state.title}</strong><small>{state.detail}</small></span></div>
      <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); setAccountOpen(true); }}><UserRound size={16} /><span>账户资料</span><ChevronRight size={14} /></button>
      <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); setAccountOpen(true); }}><Shield size={16} /><span>账户安全</span><ChevronRight size={14} /></button>
      <button type="button" role="menuitem" onClick={() => { setMenuOpen(false); openSettings(); }}><Settings size={16} /><span>应用设置</span><ChevronRight size={14} /></button>
      <button className="menu-logout" type="button" role="menuitem" disabled={auth.mode !== "authenticated" || !auth.online || !auth.serviceOnline} onClick={async () => { try { await auth.logout(); setMenuOpen(false); toast("已退出账户，回到本机游客空间"); } catch (error) { toast(error instanceof Error ? error.message : "退出失败"); } }}><LogOut size={16} /><span>{auth.online && auth.serviceOnline ? "退出登录" : "联网后退出"}</span></button>
    </div>}
    <AccountDialog open={accountOpen} onClose={() => setAccountOpen(false)} syncStatus={syncStatus} lastSyncedAt={lastSyncedAt} />
  </div>;
}

function accountState(mode: ReturnType<typeof useAuth>["mode"], syncStatus: SyncStatus, lastSyncedAt: string | null) {
  if (mode === "expired") return { icon: Shield, title: "登录已过期", detail: "本机更改仍已保留", tone: "warning" };
  if (mode === "offline-account" || syncStatus === "offline") return { icon: CloudOff, title: "账户离线可用", detail: "联网后继续同步", tone: "warning" };
  if (syncStatus === "synced") return { icon: Cloud, title: "账户数据已同步", detail: lastSyncedAt ? new Date(lastSyncedAt).toLocaleString("zh-CN", { hour: "2-digit", minute: "2-digit" }) : "刚刚", tone: "success" };
  return { icon: HardDrive, title: "账户数据保存在本机", detail: "正在建立同步", tone: "neutral" };
}

function AccountDialog({ open, onClose, syncStatus, lastSyncedAt }: { open: boolean; onClose(): void; syncStatus: SyncStatus; lastSyncedAt: string | null }) {
  const auth = useAuth();
  const { toast } = useUi();
  const [tab, setTab] = useState<"profile" | "security" | "session">("profile");
  const [name, setName] = useState(auth.user?.displayName ?? "");
  const [email, setEmail] = useState(auth.user?.email ?? "");
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const editable = auth.mode === "authenticated" && auth.online && auth.serviceOnline;

  useEffect(() => { setName(auth.user?.displayName ?? ""); setEmail(auth.user?.email ?? ""); }, [auth.user]);
  const run = async (action: () => Promise<void>, message: string) => { setPending(true);setError("");try{await action();toast(message);setCurrentPassword("");setNewPassword("");}catch(reason){setError(reason instanceof ApiError||reason instanceof Error?reason.message:"暂时无法保存");}finally{setPending(false);} };
  const saveProfile = (event: FormEvent) => { event.preventDefault();void run(() => auth.updateDisplayName(name), "称呼已更新"); };
  const saveEmail = (event: FormEvent) => { event.preventDefault();void run(() => auth.updateEmail(currentPassword,email), "邮箱已更新"); };
  const savePassword = (event: FormEvent) => { event.preventDefault();void run(() => auth.updatePassword(currentPassword,newPassword), "密码已更新，其他设备已退出"); };

  return <Modal open={open} onClose={onClose} title="账户" description="管理资料、安全设置和当前会话。" size="medium">
    <div className="account-tabs" role="tablist" aria-label="账户设置">
      {([['profile','个人资料'],['security','账户安全'],['session','当前会话']] as const).map(([id,label]) => <button key={id} type="button" role="tab" aria-selected={tab===id} className={tab===id?'active':''} onClick={() => {setTab(id);setError("");}}>{label}</button>)}
    </div>
    {!editable && <div className="account-offline-note"><CloudOff size={16} /><span>当前未连接到已验证会话。核心数据仍可使用，账户修改需要联网。</span></div>}
    {tab === "profile" && <form className="account-form" onSubmit={saveProfile}><label className="form-field"><span>称呼</span><input value={name} onChange={(event)=>setName(event.target.value)} disabled={!editable} maxLength={40}/></label><label className="form-field"><span>邮箱</span><input value={auth.user?.email??""} disabled /></label>{error&&<p className="form-error" role="alert">{error}</p>}<button className="button primary" disabled={!editable||pending||name.trim()===auth.user?.displayName}>保存称呼</button></form>}
    {tab === "security" && <div className="security-forms"><form className="account-form" onSubmit={saveEmail}><h3>修改邮箱</h3><label className="form-field"><span>新邮箱</span><input type="email" value={email} onChange={(event)=>setEmail(event.target.value)} disabled={!editable}/></label><label className="form-field"><span>当前密码</span><input type="password" value={currentPassword} onChange={(event)=>setCurrentPassword(event.target.value)} disabled={!editable} autoComplete="current-password"/></label><button className="button secondary" disabled={!editable||pending}>更新邮箱</button></form><form className="account-form" onSubmit={savePassword}><h3>修改密码</h3><label className="form-field"><span>当前密码</span><input type="password" value={currentPassword} onChange={(event)=>setCurrentPassword(event.target.value)} disabled={!editable} autoComplete="current-password"/></label><label className="form-field"><span>新密码</span><input type="password" value={newPassword} onChange={(event)=>setNewPassword(event.target.value)} disabled={!editable} autoComplete="new-password" placeholder="至少 10 个字符"/></label>{error&&<p className="form-error" role="alert">{error}</p>}<button className="button secondary" disabled={!editable||pending||newPassword.length<10}>更新密码</button></form></div>}
    {tab === "session" && <div className="session-panel"><div className="session-row"><span className="session-device"><HardDrive size={18}/></span><span><strong>当前浏览器</strong><small>{auth.mode === "authenticated" ? "已验证 · 30 天会话" : "本地缓存模式"}</small></span><b>{auth.mode === "authenticated" ? "当前" : "离线"}</b></div><dl><div><dt>同步状态</dt><dd>{syncStatus === "synced" ? "已同步" : syncStatus === "conflict" ? "已恢复服务端版本" : "等待连接"}</dd></div><div><dt>最近同步</dt><dd>{lastSyncedAt ? new Date(lastSyncedAt).toLocaleString("zh-CN") : "尚无记录"}</dd></div><div><dt>会话到期</dt><dd>{auth.expiresAt ? new Date(auth.expiresAt).toLocaleDateString("zh-CN") : "需要重新验证"}</dd></div></dl></div>}
  </Modal>;
}
