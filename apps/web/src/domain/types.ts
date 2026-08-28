export type EntityKind = "goal" | "task" | "event" | "record" | "note" | "review";
export type Area = "工作" | "健康" | "成长" | "关系" | "财务" | "生活";
export type GoalMetricType = "milestone" | "numeric" | "habit" | "project";
export type GoalStatus = "active" | "paused" | "completed" | "abandoned";
export type GoalHealth = "normal" | "attention" | "stalled";
export type TaskStatus = "todo" | "doing" | "done" | "archived";
export type Priority = "normal" | "important";
export type RecordKind = "status" | "idea" | "completion" | "inbox";
export type AgentRunStatus = "ready" | "reading" | "analyzing" | "waiting" | "applying" | "completed" | "failed" | "stopped";
export type AgentStepStatus = "pending" | "running" | "done" | "failed";
export type ActionMode = "read" | "confirm";
export type DataMode = "local" | "selected";

export interface VersionedResource {
  version: number;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string;
}

export interface Milestone extends VersionedResource {
  id: string;
  goalId: string;
  title: string;
  dueAt?: string;
  completedAt?: string;
  sortOrder: number;
}

export interface Goal extends VersionedResource {
  id: string;
  title: string;
  why: string;
  area: Area;
  metricType: GoalMetricType;
  targetValue: number;
  currentValue: number;
  unit: string;
  startAt: string;
  dueAt?: string;
  status: GoalStatus;
  health: GoalHealth;
  milestones: Milestone[];
}

export interface Task extends VersionedResource {
  id: string;
  title: string;
  status: TaskStatus;
  priority: Priority;
  estimateMinutes: number;
  dueAt?: string;
  scheduledStart?: string;
  scheduledEnd?: string;
  goalId?: string;
  sourceRecordId?: string;
  completedAt?: string;
}

export type ReminderChannel = "in_app" | "email";
export type ReminderStatus = "pending" | "delivered" | "failed" | "cancelled";

export interface CalendarReminder extends VersionedResource {
  id: string;
  eventId: string;
  offsetMinutes: number;
  channel: ReminderChannel;
  scheduledAt: string;
  status: ReminderStatus;
  deliveredAt?: string;
  attempts: number;
}

export interface CalendarEvent extends VersionedResource {
  id: string;
  title: string;
  startAt: string;
  endAt: string;
  location?: string;
  reminderMinutes: number[];
  reminders?: CalendarReminder[];
  timezone: string;
  sourceCalendar?: string;
  kind: "fixed" | "focus" | "health" | "personal";
  goalId?: string;
}

export interface RecordEntry extends VersionedResource {
  id: string;
  rawText: string;
  kind: RecordKind;
  occurredAt: string;
  mood?: number;
  energy?: number;
  tags: string[];
  parsedEntityId?: string;
  archivedAt?: string;
}

export interface Note extends VersionedResource {
  id: string;
  title: string;
  bodyMarkdown: string;
  tags: string[];
  category: "产品思考" | "阅读笔记" | "健康训练" | "生活方法" | "其他";
  linkedEntityIds: string[];
  archivedAt?: string;
}

export interface DailyReview extends VersionedResource {
  id: string;
  date: string;
  wins: string;
  blockers: string;
  mood?: number;
  energy?: number;
  tomorrowFocus: string;
  aiSummary?: string;
}

export interface SourceRef {
  id: string;
  kind: EntityKind;
  label: string;
}

export interface AgentStep {
  id: string;
  title: string;
  detail: string;
  status: AgentStepStatus;
  meta?: string;
}

export type AgentChangeType = "reschedule-task" | "create-task" | "create-event" | "archive-record" | "link-note";

export interface AgentChange {
  id: string;
  type: AgentChangeType;
  entityId?: string;
  title: string;
  before?: string;
  after: string;
  reason: string;
  sourceRefs: SourceRef[];
  status: "pending" | "accepted" | "rejected";
}

export interface AgentRun {
  id: string;
  intent: string;
  status: AgentRunStatus;
  actionMode: ActionMode;
  scope: string[];
  sourceRefs?: SourceRef[];
  steps: AgentStep[];
  changes: AgentChange[];
  startedAt: string;
  finishedAt?: string;
  summary?: string;
}

export interface AuditEvent {
  id: string;
  actor: "user" | "agent" | "system";
  action: string;
  entityRefs: string[];
  before?: string;
  after?: string;
  createdAt: string;
  undo?: UndoAction;
}

export type UndoAction =
  | { type: "restore-task"; task: Task }
  | { type: "delete-task"; taskId: string }
  | { type: "restore-event"; event: CalendarEvent }
  | { type: "delete-event"; eventId: string }
  | { type: "restore-record"; record: RecordEntry }
  | { type: "delete-record"; recordId: string }
  | { type: "restore-note"; note: Note }
  | { type: "delete-note"; noteId: string }
  | { type: "restore-goal"; goal: Goal }
  | { type: "delete-goal"; goalId: string }
  | { type: "batch"; actions: UndoAction[] };

export interface AppSettings {
  schemaVersion: number;
  version: number;
  updatedAt: string;
  energy: number;
  aiEnabled: boolean;
  remindersEnabled: boolean;
  onboardingCompleted: boolean;
  focusAreas: Area[];
  dataMode: DataMode;
  localOnly: boolean;
  permissions: {
    goals: boolean;
    calendar: boolean;
    records: boolean;
    privateNotes: boolean;
  };
}

export interface AppData {
  version: 1;
  goals: Goal[];
  tasks: Task[];
  events: CalendarEvent[];
  records: RecordEntry[];
  notes: Note[];
  reviews: DailyReview[];
  agentRuns: AgentRun[];
  audit: AuditEvent[];
  settings: AppSettings;
}

export interface CaptureDraft {
  rawText: string;
  kind: EntityKind;
  title: string;
  occurredAt: string;
  startAt?: string;
  endAt?: string;
  estimateMinutes?: number;
  goalId?: string;
  confidence: number;
  explanation: string;
  recordKind?: RecordKind;
  mood?: number;
  energy?: number;
}
