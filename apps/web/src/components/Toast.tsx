import { X } from "lucide-react";

export interface ToastItem {
  id: string;
  message: string;
  action?: { label: string; onClick(): void };
}

export function ToastViewport({ items, onDismiss }: { items: ToastItem[]; onDismiss(id: string): void }) {
  return (
    <div className="toast-viewport" aria-live="polite" aria-atomic="false">
      {items.map((item) => (
        <div className="toast" key={item.id} role="status">
          <span>{item.message}</span>
          {item.action && <button type="button" onClick={() => { item.action?.onClick(); onDismiss(item.id); }}>{item.action.label}</button>}
          <button className="toast-close" type="button" onClick={() => onDismiss(item.id)} aria-label="关闭提示"><X size={14} /></button>
        </div>
      ))}
    </div>
  );
}
