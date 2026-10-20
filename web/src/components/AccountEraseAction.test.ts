import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import AccountEraseAction from "./AccountEraseAction.vue";
import {
  chooseCommandDataset,
  commandButton,
  commandTestClient,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import { saveEraseIntent } from "./eraseIntentStorage";
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("./JobDetails.vue", () => ({
  default: { props: ["jobId"], template: "<div>原擦除任务 {{ jobId }}</div>" },
}));
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount(provide = true) {
  const client = commandTestClient();
  return {
    client,
    ...mountCommandTest(
      AccountEraseAction,
      { client: client as unknown as AeroIdClient, accountID: testAccountID },
      { provide },
    ),
  };
}
describe("self erase explicit plan", () => {
  it("does not infer all sources or create before server target confirmation", async () => {
    const { root, client } = mount();
    await flushCommandUI();
    expect(commandButton(root, "预览擦除我的账户").disabled).toBe(true);
    chooseCommandDataset(root, 2);
    await flushCommandUI();
    commandButton(root, "预览擦除我的账户").click();
    await vi.waitFor(() =>
      expect(root.textContent).toContain("服务端命令预览"),
    );
    expect(client.commands.prepare).toHaveBeenCalledWith(
      { kind: "erase", datasets: ["snaplink.identity"] },
      expect.any(String),
      testAccountID,
    );
    expect(client.eraseMyAccount).not.toHaveBeenCalled();
    expect(client.commands.execute).not.toHaveBeenCalled();
    const input = root.querySelector<HTMLInputElement>(
      'input:not([type="checkbox"])',
    )!;
    input.value = testAccountID;
    input.dispatchEvent(new Event("input"));
    await flushCommandUI();
    commandButton(root, "确认创建任务").click();
    await vi.waitFor(() =>
      expect(root.textContent).toContain(`原擦除任务 ${testReceiptID}`),
    );
    expect(client.eraseMyAccount).not.toHaveBeenCalled();
    expect(commandButton(root, "预览擦除我的账户").disabled).toBe(true);
  });
  it("blocks unbound legacy erase keys and never silently converts them to a new API", async () => {
    saveEraseIntent(sessionStorage, testAccountID, {
      idempotencyKey: testReceiptID,
    });
    const { root, client } = mount();
    await flushCommandUI();
    expect(root.textContent).toContain("旧接口存在擦除");
    expect(commandButton(root, "预览擦除我的账户").disabled).toBe(true);
    expect(client.commands.prepare).not.toHaveBeenCalled();
    expect(client.eraseMyAccount).not.toHaveBeenCalled();
  });
  it("does not allow a missing/unknown capability report to grant erase", async () => {
    const { root, client } = mount(false);
    await flushCommandUI();
    expect(commandButton(root, "预览擦除我的账户").disabled).toBe(true);
    expect(client.commands.prepare).not.toHaveBeenCalled();
  });
});
