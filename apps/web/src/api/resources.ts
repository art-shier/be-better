import { apiRequest } from "./http";

export interface ResourceMutationContext {
  deviceId: string;
  mutationId: string;
}

export interface VersionedServerResource {
  id: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string;
}

export interface ServerTag extends VersionedServerResource { name: string }
export interface ServerGoal extends VersionedServerResource {
  title: string; why: string; area: string; metricType: string; targetValue: number; currentValue: number;
  unit: string; startDate: string; dueDate?: string; status: string; health: string;
}
export interface ServerMilestone extends VersionedServerResource {
  goalId: string; title: string; dueAt?: string; completedAt?: string; sortOrder: number;
}
export interface ServerTask extends VersionedServerResource {
  title: string; status: string; priority: string; estimateMinutes: number; dueAt?: string;
  scheduledStart?: string; scheduledEnd?: string; goalId?: string; sourceRecordId?: string; completedAt?: string;
}
export interface ServerReminder extends VersionedServerResource {
  eventId: string; offsetMinutes: number; channel: string; scheduledAt: string; status: string;
  deliveredAt?: string; attempts: number;
}
export interface ServerCalendarEvent extends VersionedServerResource {
  title: string; startAt: string; endAt: string; timezone: string; location?: string; kind: string;
  sourceCalendar?: string; goalId?: string;
}
export interface ServerCalendarEventResult { event: ServerCalendarEvent; reminders: ServerReminder[] }
export interface ServerRecord extends VersionedServerResource {
  rawText: string; kind: string; occurredAt: string; mood?: number; energy?: number; archivedAt?: string; tags?: ServerTag[];
}
export interface ServerNote extends VersionedServerResource {
  title: string; bodyMarkdown: string; category: string; archivedAt?: string; tags?: ServerTag[]; linkedEntityIds?: string[];
}
export interface ServerDailyReview extends VersionedServerResource {
  reviewDate: string; wins: string; blockers: string; mood?: number; energy?: number; tomorrowFocus: string; aiSummary?: string;
}
export interface ServerUserSettings {
  schemaVersion: number;
  version: number;
  settings: Record<string, unknown>;
  updatedAt: string;
}

export interface CursorPage<T> {
  items: T[];
  nextCursor?: string;
  hasMore: boolean;
}

function mutationHeaders(context: ResourceMutationContext, version?: number): HeadersInit {
  return {
    "X-Device-ID": context.deviceId,
    "Idempotency-Key": context.mutationId,
    ...(version === undefined ? {} : { "If-Match": `"${version}"` }),
  };
}

function queryString(values: Record<string, string | number | undefined>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) if (value !== undefined && value !== "") query.set(key, String(value));
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

async function listCursorResource<T>(path: string, collection: string, options: { cursor?: string; limit?: number; [key: string]: string | number | undefined } = {}): Promise<CursorPage<T>> {
  const response = await apiRequest<Record<string, unknown> & { nextCursor?: string; hasMore: boolean }>(`${path}${queryString(options)}`);
  return { items: response[collection] as T[], nextCursor: response.nextCursor, hasMore: response.hasMore };
}

export function createResource<T>(path: string, input: unknown, context: ResourceMutationContext): Promise<T> {
  return apiRequest(path, { method: "POST", headers: mutationHeaders(context), json: input });
}

export function getResource<T>(path: string): Promise<T> {
  return apiRequest(path);
}

export function patchResource<T>(path: string, patch: unknown, version: number, context: ResourceMutationContext): Promise<T> {
  return apiRequest(path, {
    method: "PATCH",
    headers: { ...mutationHeaders(context, version), "Content-Type": "application/merge-patch+json" },
    json: patch,
  });
}

export async function deleteResource(path: string, version: number, context: ResourceMutationContext): Promise<void> {
  await apiRequest(path, { method: "DELETE", headers: mutationHeaders(context, version) });
}

export function listGoals(options: { cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerGoal>> {
  return listCursorResource("/goals", "goals", options);
}
export async function listMilestones(goalId: string): Promise<ServerMilestone[]> {
  return (await apiRequest<{ milestones: ServerMilestone[] }>(`/goals/${goalId}/milestones`)).milestones;
}
export function listTasks(options: { status?: string; cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerTask>> {
  return listCursorResource("/tasks", "tasks", options);
}
export function listCalendarEvents(options: { start?: string; end?: string; cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerCalendarEvent>> {
	return listCursorResource("/calendar-events", "events", options);
}
export function getCalendarEvent(id: string): Promise<ServerCalendarEventResult> {
  return getResource(`/calendar-events/${id}`);
}
export function listRecords(options: { cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerRecord>> {
  return listCursorResource("/records", "records", options);
}
export function listNotes(options: { q?: string; cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerNote>> {
  return listCursorResource("/notes", "notes", options);
}
export function listDailyReviews(options: { cursor?: string; limit?: number } = {}): Promise<CursorPage<ServerDailyReview>> {
	return listCursorResource("/daily-reviews", "reviews", options);
}
export async function listTags(): Promise<ServerTag[]> {
  return (await apiRequest<{ tags: ServerTag[] }>("/tags")).tags;
}
export function getUserSettings(): Promise<ServerUserSettings> {
  return apiRequest("/users/me/settings");
}
