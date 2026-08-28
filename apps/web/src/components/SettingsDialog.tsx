import { Cloud, CloudOff, Download, HardDrive, RefreshCw, RotateCcw, Upload } from "lucide-react";
import { useRef, useState } from "react";
import { useAuth } from "../auth/AuthProvider";
import { useAppStore } from "../store/AppStore";
import { useUi } from "../ui/UiProvider";
import { Modal } from "./Modal";

export function SettingsDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const auth = useAuth();
  const { data, dispatch, exportData, importData, lastSyncedAt, reset, syncStatus } = useAppStore();
  const { toast } = useUi();
  const fileRef = useRef<HTMLInputElement>(null);
  const [notificationPermission, setNotificationPermission] = useState<NotificationPermission | "unsupported">(() => "Notification" in window ? Notification.permission : "unsupported");
  const syncLabel = !auth.user ? "仅本机" : syncStatus === "synced" ? "已同步" : syncStatus === "connecting" ? "同步中" : syncStatus === "offline" ? "离线" : syncStatus === "conflict" ? "已备份冲突" : "仅本地";
  const syncDescription = !auth.user
    ? "游客空间 · 数据只保存在当前浏览器"
    : syncStatus === "synced"
    ? `本地优先 · 最近同步 ${lastSyncedAt ? new Date(lastSyncedAt).toLocaleString("zh-CN", { hour12: false }) : "刚刚"}`
    : syncStatus === "connecting"
      ? "本地数据已保存，正在连接 Go 服务"
      : syncStatus === "conflict"
        ? "本地冲突版本已备份，当前展示服务端数据"
        : "Go 服务不可用时，数据继续保存在浏览器";
  const SyncIcon = syncStatus === "synced" ? Cloud : syncStatus === "connecting" ? RefreshCw : syncStatus === "offline" ? CloudOff : HardDrive;

  const download = () => {
    const blob = new Blob([exportData()], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `dayorder-backup-${new Date().toISOString().slice(0, 10)}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
    toast("备份已导出");
  };

  const importFile = async (file?: File) => {
    if (!file) return;
    const result = importData(await file.text());
    toast(result.message);
    if (result.ok) onClose();
  };

  const toggleReminders = async () => {
    if (!("Notification" in window)) { toast("当前浏览器不支持系统通知"); return; }
    if (data.settings.remindersEnabled) { dispatch({ type: "set-reminders", value: false }); toast("浏览器提醒已关闭"); return; }
    const permission = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
    setNotificationPermission(permission);
    if (permission !== "granted") { dispatch({ type: "set-reminders", value: false }); toast("未获得通知权限，提醒保持关闭"); return; }
    dispatch({ type: "set-reminders", value: true });
    toast("浏览器提醒已开启，将按日程规则触发");
  };

  return (
    <Modal open={open} title="数据与设置" description={auth.user ? "账户数据本地优先，并在服务可用时同步。" : "游客数据仅保存在当前浏览器；注册后可选择迁移。"} onClose={onClose}>
      <div className="settings-summary"><span className="settings-icon"><SyncIcon className={syncStatus === "connecting" ? "sync-spinning" : undefined} size={21} /></span><div><strong>{syncDescription}</strong><p>{data.goals.length} 个目标 · {data.tasks.length} 个任务 · {data.notes.length} 篇笔记</p></div><span className={`status-badge ${syncStatus === "synced" ? "success" : ""}`}>{syncLabel}</span></div>
      <div className="setting-list">
        <div className="setting-row"><div><strong>AI 建议</strong><span>关闭后结构化功能仍可使用</span></div><button className={`switch ${data.settings.aiEnabled ? "on" : ""}`} type="button" role="switch" aria-checked={data.settings.aiEnabled} onClick={() => dispatch({ type: "set-ai", value: !data.settings.aiEnabled })}><span /></button></div>
        <div className="setting-row"><div><strong>浏览器提醒</strong><span>{notificationPermission === "unsupported" ? "当前浏览器不支持" : notificationPermission === "denied" ? "通知权限已被浏览器拒绝" : "按每条日程的提前量触发"}</span></div><button className={`switch ${data.settings.remindersEnabled ? "on" : ""}`} type="button" role="switch" aria-label="浏览器提醒" aria-checked={data.settings.remindersEnabled} onClick={() => void toggleReminders()}><span /></button></div>
        <div className="setting-row data-mode-row"><div><strong>数据模式</strong><span>{data.settings.dataMode === "local" ? "个人数据不离开当前设备" : "仅在你主动使用 AI 时发送选中的内容"}</span></div><div className="setting-segmented" role="radiogroup" aria-label="数据模式"><button className={data.settings.dataMode === "local" ? "active" : ""} type="button" role="radio" aria-checked={data.settings.dataMode === "local"} onClick={() => dispatch({ type: "set-data-mode", value: "local" })}>完全本地</button><button className={data.settings.dataMode === "selected" ? "active" : ""} type="button" role="radio" aria-checked={data.settings.dataMode === "selected"} onClick={() => dispatch({ type: "set-data-mode", value: "selected" })}>仅选中内容</button></div></div>
      </div>
      <div className="settings-actions">
        <button className="button secondary" type="button" onClick={download}><Download size={17} />导出备份</button>
        <button className="button secondary" type="button" onClick={() => fileRef.current?.click()}><Upload size={17} />导入备份</button>
        <input ref={fileRef} type="file" accept="application/json" hidden onChange={(event) => void importFile(event.target.files?.[0])} />
        <button className="button danger-ghost" type="button" onClick={() => { if (window.confirm("确定恢复演示数据？当前本地更改会被替换。")) { reset(); toast("已恢复演示数据"); } }}><RotateCcw size={17} />恢复演示数据</button>
      </div>
    </Modal>
  );
}
