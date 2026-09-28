import { describe, expect, it } from "vitest";

import type { ConformanceCase } from "../generated/protocol";
import { validateProtocol } from "../protocol/validate";
import { advance } from "./reducer";

const fixtureModules = import.meta.glob("../generated/conformance/*.json", {
  eager: true,
  import: "default",
}) as Record<string, unknown>;

const fixtures = Object.entries(fixtureModules)
  .sort(([left], [right]) => left.localeCompare(right))
  .map(([path, value]) => ({ path, value: validateProtocol<ConformanceCase>("ConformanceCase", value) }));

describe("runtime conformance", () => {
  it("discovers the complete canonical fixture matrix", () => {
    expect(fixtures.map(({ path }) => path.split("/").at(-1))).toEqual([
      "approval-denied.json",
      "budget-exhausted.json",
      "cancelled.json",
      "capability-unavailable.json",
      "multiple-tool-calls.json",
      "orphan-tool-use.json",
      "react-tool-success.json",
      "repeated-tool-call.json",
      "tool-call-incomplete.json",
      "tool-turn-budget-exhausted.json",
      "tool-turn-usage.json",
    ]);
  });

  it.each(fixtures)("replays every complete transition in $path", ({ value }) => {
    let state = structuredClone(value.initialState);

    value.inputs.forEach((input, index) => {
      const stateBefore = structuredClone(state);
      const inputBefore = structuredClone(input);
      const transition = advance(state, input);

      expect(transition, `${value.name} transition ${index}`).toEqual(value.expectedTransitions[index]);
      expect(state, `${value.name} transition ${index} mutated state`).toEqual(stateBefore);
      expect(input, `${value.name} transition ${index} mutated input`).toEqual(inputBefore);
      expect(transition.state).not.toBe(state);
      expect(transition.effects).not.toBe(value.expectedTransitions[index].effects);
      state = transition.state;
    });
  });
});
