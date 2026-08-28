export type EntityKind = "goal" | "task" | "event" | "record" | "note" | "review";
export type Area = "工作" | "健康" | "成长" | "关系" | "财务" | "生活";
export type GoalMetricType = "milestone" | "numeric" | "habit" | "project";
export type GoalStatus = "active" | "paused" | "completed" | "abandoned";
export type GoalHealth = "normal" | "attention" | "stalled";
export type TaskStatus = "todo" | "doing" | "done" | "archived";
export type Priority = "normal" | "important";
export type RecordKind = "status" | "idea" | "completion" | "inbox";
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
