import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import type { CalendarEvent, EntityKind, Goal, Note, RecordEntry, Task } from "../domain/types";
import { EntityDialog } from "../components/EntityDialog";
import { QuickCaptureDialog } from "../components/QuickCaptureDialog";
import { ReviewDialog } from "../components/ReviewDialog";
import { SearchDialog } from "../components/SearchDialog";
import { SettingsDialog } from "../components/SettingsDialog";
import { ToastViewport, type ToastItem } from "../components/Toast";
import { OnboardingDialog } from "../components/OnboardingDialog";

type EditorTarget =
  | { kind: "goal"; value?: Goal }
  | { kind: "task"; value?: Task }
  | { kind: "event"; value?: CalendarEvent }
  | { kind: "record"; value?: RecordEntry }
  | { kind: "note"; value?: Note };

interface UiContextValue {
  openCapture(preferred?: EntityKind): void;
  openSearch(): void;
  openSettings(): void;
  openReview(): void;
  editGoal(value?: Goal): void;
  editTask(value?: Task): void;
  editEvent(value?: CalendarEvent): void;
  editRecord(value?: RecordEntry): void;
  editNote(value?: Note): void;
  toast(message: string, action?: ToastItem["action"]): void;
}

const UiContext = createContext<UiContextValue | null>(null);

export function UiProvider({ children }: { children: ReactNode }) {
  const [capture, setCapture] = useState<EntityKind | "auto" | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [reviewOpen, setReviewOpen] = useState(false);
  const [editor, setEditor] = useState<EditorTarget | null>(null);
  const [toasts, setToasts] = useState<ToastItem[]>([]);

  const toast = useCallback((message: string, action?: ToastItem["action"]) => {
    const item = { id: `${Date.now()}_${Math.random()}`, message, action };
    setToasts((current) => [...current, item]);
    window.setTimeout(() => setToasts((current) => current.filter((toastItem) => toastItem.id !== item.id)), 3400);
  }, []);

  const value = useMemo<UiContextValue>(() => ({
    openCapture: (preferred) => setCapture(preferred ?? "auto"),
    openSearch: () => setSearchOpen(true),
    openSettings: () => setSettingsOpen(true),
    openReview: () => setReviewOpen(true),
    editGoal: (goal) => setEditor({ kind: "goal", value: goal }),
    editTask: (task) => setEditor({ kind: "task", value: task }),
    editEvent: (event) => setEditor({ kind: "event", value: event }),
    editRecord: (record) => setEditor({ kind: "record", value: record }),
    editNote: (note) => setEditor({ kind: "note", value: note }),
    toast,
  }), [toast]);

  return (
    <UiContext.Provider value={value}>
      {children}
      <OnboardingDialog />
      <QuickCaptureDialog preferred={capture} onClose={() => setCapture(null)} />
      <SearchDialog open={searchOpen} onClose={() => setSearchOpen(false)} />
      <SettingsDialog open={settingsOpen} onClose={() => setSettingsOpen(false)} />
      <ReviewDialog open={reviewOpen} onClose={() => setReviewOpen(false)} />
      <EntityDialog target={editor} onClose={() => setEditor(null)} />
      <ToastViewport items={toasts} onDismiss={(id) => setToasts((current) => current.filter((item) => item.id !== id))} />
    </UiContext.Provider>
  );
}

export function useUi(): UiContextValue {
  const context = useContext(UiContext);
  if (!context) throw new Error("useUi must be used inside UiProvider");
  return context;
}

export type { EditorTarget };
