import { apiRequest } from "./http";
import type { ResourceMutationContext } from "./resources";

export interface ServerAuditEntity {
  entityType: string;
  entityId: string;
}

export interface ServerAuditEvent {
  id: string;
  actorType: "user" | "agent" | "system";
  actorId?: string;
  action: string;
  requestId: string;
  beforeData?: unknown;
  afterData?: unknown;
  metadata: Record<string, unknown>;
  entities: ServerAuditEntity[];
  createdAt: string;
  undoable: boolean;
}

export interface AuditEventPage {
  events: ServerAuditEvent[];
  nextCursor?: string;
  hasMore: boolean;
}

export interface UndoAuditResult {
  originalAuditId: string;
  entityType: string;
  entityId: string;
  entityOperation: string;
  entityVersion: number;
  data?: unknown;
}

function mutationHeaders(context: ResourceMutationContext, version: number): HeadersInit {
  return {
    "X-Device-ID": context.deviceId,
    "Idempotency-Key": context.mutationId,
    "If-Match": `"${version}"`,
  };
}

export function listAuditEvents(options: { cursor?: string; limit?: number } = {}): Promise<AuditEventPage> {
  const query = new URLSearchParams();
  if (options.cursor) query.set("cursor", options.cursor);
  if (options.limit !== undefined) query.set("limit", String(options.limit));
  const encoded = query.toString();
  return apiRequest(`/audit-events${encoded ? `?${encoded}` : ""}`);
}

export function getAuditEvent(auditEventId: string): Promise<ServerAuditEvent> {
  return apiRequest(`/audit-events/${auditEventId}`);
}

export function undoAuditEvent(auditEventId: string, version: number, context: ResourceMutationContext): Promise<UndoAuditResult> {
  return apiRequest(`/audit-events/${auditEventId}/undo`, { method: "POST", headers: mutationHeaders(context, version) });
}

export function auditEntityVersion(event: { afterData?: unknown }): number | undefined {
  if (!event.afterData || typeof event.afterData !== "object" || Array.isArray(event.afterData)) return undefined;
  const version = (event.afterData as { version?: unknown }).version;
  return typeof version === "number" && Number.isSafeInteger(version) && version > 0 ? version : undefined;
}
