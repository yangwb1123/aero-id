import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import OperationCreateDialog from "./OperationCreateDialog.vue";
import { loadCommandPreviews } from "./commandPreviewStorage";
import {
  commandButton,
  chooseCommandDataset,
  commandTestClient,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testDatasets,
  testReceiptID,
  testScope,
  unmountCommandTests,
} from "./commandTestFixtures";
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount(provide = true) {
  const client = commandTestClient();
  const created = vi.fn();
  return {
    client,
    created,
    ...mountCommandTest(
      OperationCreateDialog,
      {
        client: client as unknown as AeroIdClient,
        datasets: testDatasets,
        defaultAccountId: testAccountID,
        onCreated: created,
      },
      { provide },
    ),
  };
}
function holdChangedPlanHash() {
  const digest = crypto.subtle.digest.bind(crypto.subtle);
  let count = 0;
  let blocked = false;
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const spy = vi
    .spyOn(crypto.subtle, "digest")
    .mockImplementation(async (...args) => {
      const result = await digest(...args);
      if (++count === 2) {
        blocked = true;
        await gate;
      }
      return result;
    });
  return {
    get blocked() {
      return blocked;
    },
    release,
    restore: () => spy.mockRestore(),
  };
}
async function open(root: HTMLElement) {
  commandButton(root, "创建 Operation").click();
  await flushCommandUI();
  chooseCommandDataset(root, 0);
  await flushCommandUI();
}
async function prepare(root: HTMLElement) {
  root
    .querySelector("form")!
    .dispatchEvent(new Event("submit", { cancelable: true, bubbles: true }));
  await flushCommandUI();
}
describe("Operation server preview", () => {
  it("preserves step order and explicit same-source datasets; creates only after server confirmation, never Runs", async () => {
    const { root, client, created } = mount();
    await open(root);
    await prepare(root);
    await vi.waitFor(() => expect(root.textContent).toContain("原意图 ID"));
    expect(client.commands.prepare).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "operation",
        operation_type: "sync",
        datasets: ["aero-im.workspaces"],
        steps: [
          expect.objectContaining({
            step_key: "source-summary-1",
            source: "aero-im",
            request_payload: { datasets: ["aero-im.workspaces"] },
          }),
        ],
      }),
      expect.any(String),
      testAccountID,
    );
    expect(client.commands.execute).not.toHaveBeenCalled();
    const input = [...root.querySelectorAll<HTMLInputElement>("input")].find(
      (item) => !item.readOnly && !item.placeholder,
    )!;
    input.value = "确认";
    input.dispatchEvent(new Event("input"));
    await flushCommandUI();
    commandButton(root, "确认创建任务").click();
    await vi.waitFor(() =>
      expect(created).toHaveBeenCalledWith({ operation_id: testReceiptID }),
    );
    expect(client.createOperation).not.toHaveBeenCalled();
    expect(client.runOperation).not.toHaveBeenCalled();
  });
  it("cannot manufacture a second key or changed command after an ambiguous prepare", async () => {
    const hash = holdChangedPlanHash();
    const { root, client } = mount();
    client.commands.prepare.mockRejectedValue(new Error("lost preview"));
    try {
      await open(root);
      await prepare(root);
      await vi.waitFor(() =>
        expect(root.textContent).toContain("lost preview"),
      );
      expect(client.commands.prepare).toHaveBeenCalledOnce();
      const original = loadCommandPreviews(sessionStorage, testScope);
      expect(original).toHaveLength(1);
      expect(original[0].idempotencyKey).toBe(
        client.commands.prepare.mock.calls[0][1],
      );
      const payload = root.querySelector("textarea")!;
      payload.value = '{"note":"changed"}';
      payload.dispatchEvent(new Event("input"));
      await prepare(root);
      await vi.waitFor(() => expect(hash.blocked).toBe(true));
      expect(root.querySelector("fieldset")!.disabled).toBe(true);
      expect(client.commands.prepare).toHaveBeenCalledOnce();
      hash.release();
      await vi.waitFor(() => {
        expect(root.querySelector("fieldset")!.disabled).toBe(false);
        expect(root.textContent).toContain("不能更改范围或换键");
      });
      expect(client.commands.prepare).toHaveBeenCalledOnce();
      expect(root.textContent).not.toContain("生成新幂等键");
      expect(loadCommandPreviews(sessionStorage, testScope)).toEqual(original);
      expect(client.commands.execute).not.toHaveBeenCalled();
      expect(client.createOperation).not.toHaveBeenCalled();
      expect(client.runOperation).not.toHaveBeenCalled();
    } finally {
      hash.release();
      await flushCommandUI();
      hash.restore();
    }
  });
  it("does not treat missing context or operation.manage alone as actual effect permissions", async () => {
    const absent = mount(false);
    await open(absent.root);
    await prepare(absent.root);
    expect(absent.client.commands.prepare).not.toHaveBeenCalled();
    const scoped = mount();
    scoped.report.value!.capabilities.find(
      (item) => item.name === "source.sync",
    )!.available = false;
    await open(scoped.root);
    await prepare(scoped.root);
    expect(scoped.client.commands.prepare).not.toHaveBeenCalled();
  });
});
