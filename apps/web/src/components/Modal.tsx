import { X } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogTitle } from "./ui/dialog";

interface ModalProps {
  open: boolean;
  title: string;
  description?: string;
  onClose(): void;
  children: ReactNode;
  footer?: ReactNode;
  size?: "small" | "medium" | "large";
  dismissible?: boolean;
  onEscape?(): boolean | void;
}

export function Modal({ open, title, description, onClose, children, footer, size = "medium", dismissible = true, onEscape }: ModalProps) {
  useEffect(() => {
    if (!open) return;
    document.body.classList.add("modal-open");
    return () => document.body.classList.remove("modal-open");
  }, [open]);

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next && dismissible) onClose(); }}>
      <DialogPortal>
        <DialogOverlay>
          <DialogContent
            className={`modal modal-${size}`}
            {...description ? {} : { "aria-describedby": undefined }}
            onOpenAutoFocus={(event) => {
              const target = (event.currentTarget as HTMLElement).querySelector<HTMLElement>("[data-autofocus]");
              if (!target) return;
              event.preventDefault();
              target.focus();
            }}
            onEscapeKeyDown={(event) => { if (!dismissible || onEscape?.() === false) event.preventDefault(); }}
            onPointerDownOutside={(event) => { if (!dismissible) event.preventDefault(); }}
          >
            <div className="modal-head">
              <div>
                <DialogTitle asChild><h2>{title}</h2></DialogTitle>
                {description && <DialogDescription asChild><p>{description}</p></DialogDescription>}
              </div>
              {dismissible && <DialogClose asChild><button className="close-button" type="button" aria-label="关闭"><X size={18} /></button></DialogClose>}
            </div>
            <div className="modal-body">{children}</div>
            {footer && <div className="modal-footer">{footer}</div>}
          </DialogContent>
        </DialogOverlay>
      </DialogPortal>
    </Dialog>
  );
}
