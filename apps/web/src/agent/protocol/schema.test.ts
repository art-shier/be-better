import { describe, expect, it } from "vitest";

import { createSchemaValidator } from "./schema";

describe("createSchemaValidator", () => {
  it("asserts UUID, date-time, and UTF-8 byte constraints", () => {
    const validate = createSchemaValidator().compile({
      type: "object",
      properties: {
        id: { type: "string", format: "uuid" },
        at: { type: "string", format: "date-time" },
        cursor: { type: "string", maxUtf8Bytes: 4_096 },
      },
      required: ["id", "at", "cursor"],
    });
    const valid = {
      id: "550e8400-e29b-41d4-a716-446655440000",
      at: "2026-09-05T00:00:00Z",
      cursor: "日".repeat(1_365),
    };

    expect(validate(valid)).toBe(true);
    expect(validate({ ...valid, id: "not-a-uuid" })).toBe(false);
    expect(validate({ ...valid, at: "2026-02-30T00:00:00Z" })).toBe(false);
    expect(validate({ ...valid, cursor: "日".repeat(1_366) })).toBe(false);
  });

  it("rejects an invalid maxUtf8Bytes schema value", () => {
    expect(() => createSchemaValidator().compile({ type: "string", maxUtf8Bytes: -1 })).toThrow();
  });
});
