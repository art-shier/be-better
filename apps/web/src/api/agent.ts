import { apiRequest } from "./http";
import type { ResourceMutationContext } from "./resources";

export type AgentScopeDomain = "goals" | "tasks" | "calendar" | "records" | "notes";
export type ServerAgentRunStatus = "ready" | "reading" | "analyzing" | "waiting" | "applying" | "completed" | "failed" | "stopped";
export type ServerAgentStepStatus = "pending" | "running" | "done" | "failed";
export type ServerAgentChangeStatus = "pending" | "accepted" | "rejected" | "applied" | "failed" | "conflicted";

export interface AgentScope {
  domains: AgentScopeDomain[];
  entityIds?: string[];
  from?: string;
  to?: string;
}

export interface ServerAgentStep {
  id: string;
  runId: string;
  sequenceNo: number;
  title: string;
  detail: string;
  status: ServerAgentStepStatus;
  metadata: Record<string, unknown>;
  startedAt?: string;
  finishedAt?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ServerAgentChange {
  id: string;
  runId: string;
  changeType: string;
  targetType: string;
  targetId?: string;
  baseVersion?: number;
  patch: unknown[];
  previewBefore?: unknown;
  previewAfter?: unknown;
  reason: string;
  status: ServerAgentChangeStatus;
  acceptedAt?: string;
  appliedAt?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface ServerAgentSourceRef {
  id: string;
  runId: string;
  entityType: string;
  entityId: string;
  entityVersion: number;
  labelSnapshot: string;
  createdAt: string;
}

export interface ServerAgentRun {
  id: string;
  intent: string;
  status: ServerAgentRunStatus;
  actionMode: "read" | "confirm";
  scope: AgentScope;
  provider?: string;
  model?: string;
  startedAt?: string;
  finishedAt?: string;
  summary?: string;
  errorCode?: string;
  errorMessage?: string;
  version: number;
  createdAt: string;
  updatedAt: string;
  steps: ServerAgentStep[];
  changes: ServerAgentChange[];
  sourceRefs: ServerAgentSourceRef[];
}

export interface AgentRunPage {
  runs: ServerAgentRun[];
  nextCursor?: string;
  hasMore: boolean;
}

export interface CreateAgentRunInput {
  intent: string;
  actionMode: "read" | "confirm";
  scope: AgentScope;
}

export interface AgentChangeResult {
  change: ServerAgentChange;
  run: ServerAgentRun;
}

function mutationHeaders(context: ResourceMutationContext, version?: number): HeadersInit {
  return {
    "X-Device-ID": context.deviceId,
    "Idempotency-Key": context.mutationId,
    ...(version === undefined ? {} : { "If-Match": `"${version}"` }),
  };
}

export function listAgentRuns(options: { cursor?: string; limit?: number } = {}): Promise<AgentRunPage> {
  const query = new URLSearchParams();
  if (options.cursor) query.set("cursor", options.cursor);
  if (options.limit !== undefined) query.set("limit", String(options.limit));
  const encoded = query.toString();
  return apiRequest(`/agent-runs${encoded ? `?${encoded}` : ""}`);
}

export function createAgentRun(input: CreateAgentRunInput, context: ResourceMutationContext): Promise<ServerAgentRun> {
  return apiRequest("/agent-runs", { method: "POST", headers: mutationHeaders(context), json: input });
}

export function getAgentRun(runId: string): Promise<ServerAgentRun> {
  return apiRequest(`/agent-runs/${runId}`);
}

export function stopAgentRun(runId: string, version: number, context: ResourceMutationContext): Promise<ServerAgentRun> {
  return apiRequest(`/agent-runs/${runId}/stop`, { method: "POST", headers: mutationHeaders(context, version) });
}

export function acceptAgentChange(changeId: string, version: number, context: ResourceMutationContext): Promise<AgentChangeResult> {
  return apiRequest(`/agent-changes/${changeId}/accept`, { method: "POST", headers: mutationHeaders(context, version) });
}

export function rejectAgentChange(changeId: string, version: number, context: ResourceMutationContext): Promise<AgentChangeResult> {
  return apiRequest(`/agent-changes/${changeId}/reject`, { method: "POST", headers: mutationHeaders(context, version) });
}
