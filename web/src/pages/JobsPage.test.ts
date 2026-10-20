import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import JobsPage from "./JobsPage.vue";
import {
  chooseCommandDataset,
  commandButton,
  commandTestClient,
  commandTestReport,
  executedTestIntent,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  unmountCommandTests,
} from "../components/commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("../components/PagedRecordTable.vue", () => ({
  default: { template: "<div>任务列表</div>" },
}));
vi.mock("../components/JobDetails.vue", () => ({
  default: { props: ["jobId"], template: "<div>原任务 {{ jobId }}</div>" },
}));
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount(provide = true) {
  const client = commandTestClient();
  return {
    client,
    ...mountCommandTest(
      JobsPage,
      { client: client as unknown as AeroIdClient },
      { provide },
    ),
  };
}
async function prepare(
  root: HTMLElement,
  client: ReturnType<typeof commandTestClient>,
) {
  chooseCommandDataset(root, 0);
  await flushCommandUI();
  commandButton(root, "创建加密导出").click();
  await vi.waitFor(() =>
    expect(client.commands.prepare).toHaveBeenCalledOnce(),
  );
  await flushCommandUI();
}
describe("JobsPage explicit scope and preview", () => {
  it("starts with no hidden full scope and prepares only the chosen local references", async () => {
    const { root, client } = mount();
    await flushCommandUI();
    expect(commandButton(root, "创建加密导出").disabled).toBe(true);
    await prepare(root, client);
    expect(client.commands.prepare).toHaveBeenCalledWith(
      { kind: "export", datasets: ["aero-vault.usage"] },
      expect.any(String),
      testAccountID,
    );
    expect(root.textContent).toContain("服务端命令预览");
    expect(client.commands.execute).not.toHaveBeenCalled();
    expect(client.createExportJob).not.toHaveBeenCalled();
    expect(client.createSyncJob).not.toHaveBeenCalled();
  });
  it("keeps the original key after ambiguous prepare and never auto-retries", async () => {
    const { root, client } = mount();
    client.commands.prepare.mockRejectedValue(new Error("network timeout"));
    await flushCommandUI();
    await prepare(root, client);
    commandButton(root, "创建加密导出").click();
    await vi.waitFor(() =>
      expect(client.commands.prepare).toHaveBeenCalledTimes(2),
    );
    expect(client.commands.prepare.mock.calls[1]).toEqual(
      client.commands.prepare.mock.calls[0],
    );
    expect(client.commands.execute).not.toHaveBeenCalled();
  });
  it("fails closed without injected capability/account context or after current grant revocation", async () => {
    const absent = mount(false);
    await flushCommandUI();
    chooseCommandDataset(absent.root, 0);
    await flushCommandUI();
    expect(commandButton(absent.root, "创建加密导出").disabled).toBe(true);
    const current = mount();
    await flushCommandUI();
    chooseCommandDataset(current.root, 0);
    current.report.value = { ...commandTestReport(), capabilities: [] };
    await flushCommandUI();
    expect(commandButton(current.root, "创建加密导出").disabled).toBe(true);
    expect(current.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("restores a known intent via GET only after refresh, including an executed original receipt", async () => {
    const first = mount();
    await flushCommandUI();
    await prepare(first.root, first.client);
    const intent = await first.client.commands.prepare.mock.results[0].value;
    first.app.unmount();
    const restored = mount();
    restored.client.commands.get.mockResolvedValue(executedTestIntent(intent));
    await flushCommandUI();
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
    commandButton(restored.root, "查询原意图 / 回执").click();
    await vi.waitFor(() =>
      expect(restored.root.textContent).toContain("原任务"),
    );
    expect(restored.client.commands.get).toHaveBeenCalledOnce();
    expect(restored.client.commands.execute).not.toHaveBeenCalled();
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
  });
});
