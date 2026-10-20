import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import AdminAccountActions from "./AdminAccountActions.vue";
import {
  chooseCommandDataset,
  commandButton,
  commandTestClient,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testDatasets,
  testOtherAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("./JobDetails.vue", () => ({
  default: { props: ["jobId"], template: "<div>原任务 {{ jobId }}</div>" },
}));
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount(provide = true) {
  const client = commandTestClient();
  return {
    client,
    ...mountCommandTest(
      AdminAccountActions,
      {
        client: client as unknown as AeroIdClient,
        accountID: testOtherAccountID,
        datasets: testDatasets,
      },
      { provide },
    ),
  };
}
async function preview(
  root: HTMLElement,
  client: ReturnType<typeof commandTestClient>,
  label = "预览账户导出",
) {
  chooseCommandDataset(root, 0);
  await flushCommandUI();
  commandButton(root, label).click();
  await vi.waitFor(() => expect(root.textContent).toContain("服务端命令预览"));
  expect(client.commands.execute).not.toHaveBeenCalled();
}
function confirm(root: HTMLElement, value: string) {
  const input = root.querySelector<HTMLInputElement>(
    'input:not([type="checkbox"])',
  )!;
  input.value = value;
  input.dispatchEvent(new Event("input"));
}
describe("administrator target preview", () => {
  it("requires an explicit nonempty selection and confirms only the server's original target receipt", async () => {
    const { root, client } = mount();
    await flushCommandUI();
    expect(commandButton(root, "预览所选同步").disabled).toBe(true);
    await preview(root, client);
    expect(client.commands.prepare).toHaveBeenCalledWith(
      { kind: "export", datasets: ["aero-vault.usage"] },
      expect.any(String),
      testOtherAccountID,
    );
    confirm(root, "确认");
    await flushCommandUI();
    commandButton(root, "确认创建任务").click();
    await vi.waitFor(() =>
      expect(root.textContent).toContain(`原任务 ${testReceiptID}`),
    );
    expect(client.createAdminAccountJob).not.toHaveBeenCalled();
    expect(client.runOperation).not.toHaveBeenCalled();
  });
  it("requires exact target confirmation for erase; actor's own account cannot confirm another target", async () => {
    const { root, client } = mount();
    await flushCommandUI();
    await preview(root, client, "预览账户擦除");
    confirm(root, testAccountID);
    await flushCommandUI();
    expect(commandButton(root, "确认创建任务").disabled).toBe(true);
    confirm(root, testOtherAccountID);
    await flushCommandUI();
    commandButton(root, "确认创建任务").click();
    await vi.waitFor(() =>
      expect(client.commands.execute).toHaveBeenCalledOnce(),
    );
    expect(client.createAdminAccountJob).not.toHaveBeenCalled();
  });
  it("pauses every action without current grants and does not grant from the administrative menu", async () => {
    const { root, client } = mount(false);
    chooseCommandDataset(root, 0);
    await flushCommandUI();
    for (const label of ["预览所选同步", "预览账户导出", "预览账户擦除"])
      expect(commandButton(root, label).disabled).toBe(true);
    expect(client.commands.prepare).not.toHaveBeenCalled();
  });
  it("keeps the same server intent and confirmation on an ambiguous execute", async () => {
    const { root, client } = mount();
    client.commands.execute.mockRejectedValue(new Error("lost execute reply"));
    await flushCommandUI();
    await preview(root, client);
    confirm(root, "确认");
    await flushCommandUI();
    commandButton(root, "确认创建任务").click();
    await flushCommandUI();
    expect(client.commands.execute).toHaveBeenCalledOnce();
    commandButton(root, "确认创建任务").click();
    await flushCommandUI();
    expect(client.commands.execute.mock.calls[1][0]).toEqual(
      client.commands.execute.mock.calls[0][0],
    );
    expect(client.commands.prepare).toHaveBeenCalledOnce();
    expect(client.createAdminAccountJob).not.toHaveBeenCalled();
  });
});
