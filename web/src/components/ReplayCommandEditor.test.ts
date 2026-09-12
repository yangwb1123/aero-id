import { describe, expect, it } from "vitest";
import { parseReplayCommands, type ReplayCommandDraft } from "./replayHelpers";

function draft(payload: string): ReplayCommandDraft {
  return {
    id: "command-1",
    source: "aero-vault",
    action: "summary",
    payload,
  };
}

describe("parseReplayCommands", () => {
  it("accepts only object payloads for allow-listed command drafts", () => {
    expect(parseReplayCommands([draft('{"limit":10}')])).toEqual([
      {
        source: "aero-vault",
        action: "summary",
        payload: { limit: 10 },
      },
    ]);
  });

  it("rejects malformed and array payloads before a replay request", () => {
    expect(() => parseReplayCommands([draft("[")])).toThrow("不是有效 JSON");
    expect(() => parseReplayCommands([draft("[]")])).toThrow(
      "必须是 JSON 对象",
    );
  });
});
