import { describe, expect, it, vi } from "vitest";
import { auditEntityVersion, getAuditEvent, listAuditEvents, undoAuditEvent } from "./audit";

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { "Content-Type": "application/json" },
});

describe("audit API client", () => {
  it("审计列表和详情使用游标协议", async () => {
    const auditId = crypto.randomUUID();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ events: [], nextCursor: "next", hasMore: false }))
      .mockResolvedValueOnce(jsonResponse({ id: auditId, action: "agent.change.apply" }));
    vi.stubGlobal("fetch", fetchMock);

    await listAuditEvents({ cursor: "audit cursor", limit: 8 });
    await getAuditEvent(auditId);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/audit-events?cursor=audit+cursor&limit=8");
    expect(fetchMock.mock.calls[1][0]).toBe(`/api/v1/audit-events/${auditId}`);
  });

  it("从受控审计后值读取撤销版本并携带写命令头", async () => {
    const auditId = crypto.randomUUID();
    const deviceId = crypto.randomUUID();
    const mutationId = crypto.randomUUID();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ originalAuditId: auditId, entityVersion: 4 }));
    vi.stubGlobal("fetch", fetchMock);

    expect(auditEntityVersion({ afterData: { id: crypto.randomUUID(), version: 3 } })).toBe(3);
    expect(auditEntityVersion({ afterData: { version: "3" } })).toBeUndefined();
    await undoAuditEvent(auditId, 3, { deviceId, mutationId });

    const headers = new Headers((fetchMock.mock.calls[0][1] as RequestInit).headers);
    expect(headers.get("X-Device-ID")).toBe(deviceId);
    expect(headers.get("Idempotency-Key")).toBe(mutationId);
    expect(headers.get("If-Match")).toBe('"3"');
  });
});
