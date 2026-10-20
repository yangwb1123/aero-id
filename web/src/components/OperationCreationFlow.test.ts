import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CommandPlan } from "../api/commandIntent";
import OperationCreateDialog from "./OperationCreateDialog.vue";
import { loadCommandPreviews } from "./commandPreviewStorage";
import {
  chooseCommandDataset,
  commandButton,
  commandTestClient,
  commandTestIntent,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testDatasets,
  testScope,
  unmountCommandTests,
} from "./commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount() {
  const client = commandTestClient();
  const created = vi.fn();
  return {
    client,
    created,
    ...mountCommandTest(OperationCreateDialog, {
      client: client as unknown as AeroIdClient,
      datasets: testDatasets,
      defaultAccountId: testAccountID,
      onCreated: created,
    }),
  };
}
async function open(root: HTMLElement) {
  commandButton(root, "创建 Operation").click();
  await flushCommandUI();
}
function drafts(root: HTMLElement) {
  return [...root.querySelectorAll<HTMLElement>("[data-operation-draft]")];
}
async function source(step: HTMLElement, value: string) {
  const input = step.querySelector<HTMLSelectElement>("select")!;
  input.value = value;
  input.dispatchEvent(new Event("change"));
  await flushCommandUI();
}
async function select(step: HTMLElement) {
  chooseCommandDataset(step, 0);
  await flushCommandUI();
}
async function submit(root: HTMLElement) {
  root
    .querySelector("form")!
    .dispatchEvent(new Event("submit", { cancelable: true, bubbles: true }));
  await flushCommandUI();
}
async function input(
  element: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  element.value = value;
  element.dispatchEvent(new Event("input"));
  await flushCommandUI();
}
async function addVault(root: HTMLElement) {
  commandButton(root, "添加步骤").click();
  await flushCommandUI();
  await source(drafts(root)[1], "aero-vault");
  await select(drafts(root)[1]);
}
function expectNoExecution(client: ReturnType<typeof commandTestClient>) {
  expect(client.commands.execute).not.toHaveBeenCalled();
  expect(client.createOperation).not.toHaveBeenCalled();
  expect(client.runOperation).not.toHaveBeenCalled();
}

describe("Operation business creation flow", () => {
  it("requires explicit dataset choice, folds expert inputs and explains why preview is paused", async () => {
    const { root, client } = mount();
    await open(root);
    expect(
      root.querySelectorAll('input[type="checkbox"]:checked'),
    ).toHaveLength(0);
    expect(commandButton(root, "获取服务端预览").disabled).toBe(true);
    expect(root.textContent).toContain("请选择本步骤的数据集");
    expect(root.textContent).toContain("IM 工作空间");
    for (const field of root.querySelectorAll(
      "textarea, input[maxlength='128']",
    )) {
      expect(field.closest("details")).not.toBeNull();
      expect(field.closest("details")!.open).toBe(false);
    }
    await submit(root);
    expect(client.commands.prepare).not.toHaveBeenCalled();
    await select(drafts(root)[0]);
    expect(commandButton(root, "获取服务端预览").disabled).toBe(false);
    expect(client.commands.prepare).not.toHaveBeenCalled();
    expectNoExecution(client);
  });

  it("selects two sources without JSON, preserves explicit order and stable keys, then confirms only the original receipt", async () => {
    const { root, client, created } = mount();
    await open(root);
    await select(drafts(root)[0]);
    await addVault(root);
    const originalKeys = drafts(root).map(
      (step) => step.dataset.operationDraft,
    );
    commandButton(drafts(root)[1], "上移").click();
    await flushCommandUI();
    expect(drafts(root).map((step) => step.dataset.operationDraft)).toEqual(
      [...originalKeys].reverse(),
    );
    await submit(root);
    await vi.waitFor(() =>
      expect(client.commands.prepare).toHaveBeenCalledOnce(),
    );
    expect(client.commands.prepare.mock.calls[0][0]).toEqual({
      kind: "operation",
      operation_type: "sync",
      datasets: ["aero-im.workspaces", "aero-vault.usage"],
      request_payload: {},
      steps: [
        {
          step_key: "source-summary-2",
          source: "aero-vault",
          action: "summary",
          request_payload: { datasets: ["aero-vault.usage"] },
        },
        {
          step_key: "source-summary-1",
          source: "aero-im",
          action: "summary",
          request_payload: { datasets: ["aero-im.workspaces"] },
        },
      ],
    });
    await vi.waitFor(() =>
      expect(root.querySelector("[data-operation-plan]")).not.toBeNull(),
    );
    const summary = root.querySelector("[data-operation-plan]")!;
    expect(summary.textContent).toContain("Vault 用量");
    expect(summary.textContent).toContain("IM 工作空间");
    expect(summary.querySelector("pre")).toBeNull();
    expectNoExecution(client);
    const confirm = [...root.querySelectorAll<HTMLInputElement>("input")].find(
      (item) => !item.readOnly,
    )!;
    await input(confirm, "确认");
    commandButton(root, "确认创建任务").click();
    await vi.waitFor(() => expect(created).toHaveBeenCalledOnce());
    expect(client.commands.execute).toHaveBeenCalledOnce();
    expect(client.createOperation).not.toHaveBeenCalled();
    expect(client.runOperation).not.toHaveBeenCalled();
  });

  it("source changes explicitly clear only the selection, not the step identity or expert parameters", async () => {
    const { root, client } = mount();
    await open(root);
    const step = drafts(root)[0];
    const key = step.dataset.operationDraft;
    await input(
      step.querySelector("textarea")!,
      '{"datasets":["aero-im.workspaces"],"note":"expert-note"}',
    );
    await source(step, "aero-vault");
    expect(drafts(root)[0].dataset.operationDraft).toBe(key);
    expect(
      JSON.parse(drafts(root)[0].querySelector("textarea")!.value),
    ).toEqual({ datasets: [], note: "expert-note" });
    expect(
      drafts(root)[0].querySelector("[hint]")!.getAttribute("hint"),
    ).toContain("切换来源会清空本步选择");
    expect(commandButton(root, "获取服务端预览").disabled).toBe(true);
    await select(drafts(root)[0]);
    await submit(root);
    await vi.waitFor(() =>
      expect(client.commands.prepare).toHaveBeenCalledOnce(),
    );
    expect(client.commands.prepare.mock.calls[0][0].steps![0]).toEqual({
      step_key: "source-summary-1",
      source: "aero-vault",
      action: "summary",
      request_payload: { datasets: ["aero-vault.usage"], note: "expert-note" },
    });
    expectNoExecution(client);
  });

  it("removing and adding steps never renames survivors or creates duplicate Step Keys", async () => {
    const { root, client } = mount();
    await open(root);
    await select(drafts(root)[0]);
    await addVault(root);
    const survivor = drafts(root)[1].dataset.operationDraft;
    commandButton(drafts(root)[0], "移除步骤").click();
    await flushCommandUI();
    commandButton(root, "添加步骤").click();
    await flushCommandUI();
    expect(drafts(root)[0].dataset.operationDraft).toBe(survivor);
    expect(
      new Set(drafts(root).map((step) => step.dataset.operationDraft)).size,
    ).toBe(2);
    expect(drafts(root)[1].querySelectorAll("input:checked")).toHaveLength(0);
    await select(drafts(root)[1]);
    await submit(root);
    await vi.waitFor(() =>
      expect(client.commands.prepare).toHaveBeenCalledOnce(),
    );
    expect(client.commands.prepare.mock.calls[0][0].steps![0].step_key).toBe(
      survivor,
    );
    expectNoExecution(client);
  });

  it.each([
    "{",
    '{"datasets":["aero-vault.usage"]}',
    '{"datasets":["aero-im.unknown"]}',
    '{"datasets":["aero-im.workspaces","aero-im.workspaces"]}',
  ])(
    "invalid expert payload %s is not silently repaired by checkboxes or source change",
    async (payload) => {
      const { root, client } = mount();
      await open(root);
      await input(drafts(root)[0].querySelector("textarea")!, payload);
      expect(
        drafts(root)[0].querySelector<HTMLInputElement>(
          'input[type="checkbox"]',
        )!.disabled,
      ).toBe(true);
      expect(
        drafts(root)[0].querySelector<HTMLSelectElement>("select")!.disabled,
      ).toBe(true);
      expect(commandButton(root, "获取服务端预览").disabled).toBe(true);
      await submit(root);
      expect(drafts(root)[0].querySelector("textarea")!.value).toBe(payload);
      expect(client.commands.prepare).not.toHaveBeenCalled();
      expectNoExecution(client);
    },
  );

  it("expert destructive compensation remains explicit and requires exact account confirmation and current qualification", async () => {
    const { root, client, report } = mount();
    await open(root);
    await select(drafts(root)[0]);
    const selects =
      drafts(root)[0].querySelectorAll<HTMLSelectElement>("select");
    selects[2].value = "erase";
    selects[2].dispatchEvent(new Event("change"));
    await flushCommandUI();
    expect(root.textContent).toContain("补偿：擦除所选来源数据");
    await submit(root);
    await vi.waitFor(() =>
      expect(root.querySelector("[data-operation-plan]")).not.toBeNull(),
    );
    expect(root.querySelector("[data-operation-plan]")!.textContent).toContain(
      "erase",
    );
    const confirm = [...root.querySelectorAll<HTMLInputElement>("input")].find(
      (item) => !item.readOnly,
    )!;
    await input(confirm, "确认");
    expect(commandButton(root, "确认创建任务").disabled).toBe(true);
    await input(confirm, testAccountID);
    expect(commandButton(root, "确认创建任务").disabled).toBe(false);
    report.value!.capabilities.find(
      (item) => item.name === "account.erase",
    )!.available = false;
    await flushCommandUI();
    expect(commandButton(root, "确认创建任务").disabled).toBe(true);
    commandButton(root, "确认创建任务").click();
    expectNoExecution(client);
  });

  it("summary enablement and current action refusal still stop the normal selected flow", async () => {
    const { root, client, report } = mount();
    await open(root);
    await select(drafts(root)[0]);
    report.value!.datasets.find(
      (item) => item.name === "aero-im.workspaces",
    )!.enabled = false;
    await flushCommandUI();
    expect(commandButton(root, "获取服务端预览").disabled).toBe(true);
    await submit(root);
    expect(client.commands.prepare).not.toHaveBeenCalled();
    report.value!.datasets.find(
      (item) => item.name === "aero-im.workspaces",
    )!.enabled = true;
    report.value!.capabilities.find(
      (item) => item.name === "source.sync",
    )!.available = false;
    await flushCommandUI();
    await submit(root);
    expect(client.commands.prepare).not.toHaveBeenCalled();
    expectNoExecution(client);
  });

  it("ambiguous prepare retains its exact selection/key; changing a checkbox cannot issue a replacement plan", async () => {
    const { root, client } = mount();
    client.commands.prepare.mockRejectedValue(new Error("lost preview"));
    await open(root);
    await select(drafts(root)[0]);
    await submit(root);
    await vi.waitFor(() => expect(root.textContent).toContain("lost preview"));
    const records = loadCommandPreviews(sessionStorage, testScope);
    await source(drafts(root)[0], "aero-vault");
    await select(drafts(root)[0]);
    await submit(root);
    await vi.waitFor(() =>
      expect(root.textContent).toContain("不能更改范围或换键"),
    );
    expect(client.commands.prepare).toHaveBeenCalledOnce();
    expect(loadCommandPreviews(sessionStorage, testScope)).toEqual(records);
    expectNoExecution(client);
  });

  it("remount recovery GET shows the original ordered scope and never prepares or runs a new command", async () => {
    const first = mount();
    await open(first.root);
    await select(drafts(first.root)[0]);
    await addVault(first.root);
    let original: ReturnType<typeof commandTestIntent> | undefined;
    first.client.commands.prepare.mockImplementation(
      async (plan: CommandPlan) => {
        original = commandTestIntent(plan);
        return original;
      },
    );
    await submit(first.root);
    await vi.waitFor(() =>
      expect(first.root.querySelector("[data-operation-plan]")).not.toBeNull(),
    );
    const records = loadCommandPreviews(sessionStorage, testScope);
    unmountCommandTests();
    const recovered = mount();
    recovered.client.commands.get.mockImplementation(async () => original!);
    await open(recovered.root);
    commandButton(recovered.root, "查询原意图").click();
    await vi.waitFor(() =>
      expect(
        recovered.root.querySelector("[data-operation-plan]"),
      ).not.toBeNull(),
    );
    expect(
      recovered.root.querySelector("[data-operation-plan]")!.textContent,
    ).toContain("IM 工作空间");
    expect(
      recovered.root.querySelector("[data-operation-plan]")!.textContent,
    ).toContain("Vault 用量");
    expect(recovered.client.commands.get).toHaveBeenCalledWith(
      original!.intent_id,
      expect.any(AbortSignal),
    );
    expect(recovered.client.commands.prepare).not.toHaveBeenCalled();
    expect(loadCommandPreviews(sessionStorage, testScope)).toEqual(records);
    expectNoExecution(recovered.client);
  });
});
