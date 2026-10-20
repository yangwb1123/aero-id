import assert from "node:assert/strict";
import { creationIntentID } from "./operation-creation-data.mjs";
import { operationID } from "./operation-results-data.mjs";

export async function runOperationCreationScenarios({
  page,
  origin,
  requests,
  setScenario,
  checkpoint,
  save,
  facts,
}) {
  let passed = 0;
  let visit = 0;
  const form = page.locator(".operation-create-form");
  const drafts = page.locator("[data-operation-draft]");
  const summary = page.locator("[data-operation-plan]");
  const posts = () =>
    requests.filter(
      (item) => item.scenario.startsWith("creation-") && item.method === "POST",
    );
  const scenarioPosts = (name) =>
    posts().filter((item) => item.scenario === name);
  const check = async (name) => {
    const dialogs = page.locator(
      '[data-iris-dialog-backdrop] > [aria-modal="true"]',
    );
    // Capture the settled real dialog, not its transient fade/scale frame.
    await dialogs.evaluateAll((elements) =>
      Promise.all(
        elements.flatMap((element) =>
          element.getAnimations().map((animation) => animation.finished),
        ),
      ),
    );
    await dialogs.evaluateAll((elements) =>
      elements.forEach((element) => {
        element.scrollTop = 0;
      }),
    );
    await checkpoint(name);
    passed += 1;
  };
  async function load(name, { clear = true } = {}) {
    setScenario(name);
    if (clear) await page.evaluate(() => sessionStorage.clear());
    await page.goto(
      `${origin}/browser-fixtures/operation-results.html?case=${name}&visit=${++visit}#/operations`,
      { waitUntil: "networkidle", timeout: 30000 },
    );
    await page
      .getByRole("button", { name: "创建 Operation / 核实原意图", exact: true })
      .click();
    await form.waitFor();
  }
  async function chooseSource(index, name) {
    await drafts.nth(index).getByLabel(/^来源/).click();
    await page.getByRole("option", { name, exact: true }).click();
  }
  async function select(index, name) {
    const checkbox = drafts
      .nth(index)
      .getByRole("checkbox", { name: new RegExp(name) });
    // Published Iris uses a zero-size native input; click its visible label,
    // exactly as a user does, without force or synthetic change events.
    await checkbox.locator("..").click();
    assert.equal(await checkbox.isChecked(), true);
  }
  async function addVault() {
    await page.getByRole("button", { name: /^添加步骤/ }).click();
    await chooseSource(1, "Aero Vault");
    await select(1, "Vault 用量");
    await select(1, "Vault 桶引用");
  }
  async function prepare() {
    await page
      .getByRole("button", { name: "获取服务端预览", exact: true })
      .click();
    await summary.waitFor();
  }
  async function confirm(value = "确认") {
    await page
      .getByRole("textbox", {
        name: value === "确认" ? "输入“确认”继续" : "输入目标账户 ID 确认",
        exact: true,
      })
      .fill(value);
    await page
      .getByRole("button", {
        name: "确认创建任务 / Operation 回执",
        exact: true,
      })
      .click();
  }
  await page.setViewportSize({ width: 1440, height: 1100 });
  await load("creation-normal");
  assert.equal(await form.getByRole("checkbox", { checked: true }).count(), 0);
  assert.equal(
    await page
      .getByRole("button", { name: "获取服务端预览", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(await form.locator("textarea:visible").count(), 0);
  assert((await drafts.first().innerText()).includes("请选择本步骤的数据集"));
  await check("creation-empty-scope-no-json-default");
  await select(0, "IM 工作空间");
  await addVault();
  await drafts
    .nth(1)
    .getByRole("button", { name: "上移", exact: true })
    .click();
  assert.deepEqual(
    await drafts.evaluateAll((elements) =>
      elements.map((element) => element.getAttribute("data-operation-draft")),
    ),
    ["source-summary-2", "source-summary-1"],
  );
  assert.equal(
    scenarioPosts("creation-normal").length,
    0,
    "selection/reorder never submit",
  );
  await check("creation-explicit-scope-two-sources-without-json");
  await prepare();
  const rows = await summary.locator(":scope > ol > li").allInnerTexts();
  assert.equal(rows.length, 2);
  assert(
    rows[0].includes("Aero Vault") &&
      rows[0].includes("Vault 用量") &&
      rows[0].includes("Vault 桶引用"),
  );
  assert(rows[1].includes("IM 工作空间"));
  assert.equal(
    await page.locator("pre:visible").count(),
    0,
    "expert JSON is folded in preview too",
  );
  assert.equal(scenarioPosts("creation-normal").length, 1);
  await check("creation-readable-server-plan-original-order");
  await confirm();
  await page
    .locator("[data-operation-summary]")
    .getByText("等待运行", { exact: true })
    .waitFor();
  assert.equal(
    scenarioPosts("creation-normal").length,
    2,
    "one preparation and one confirmed execute only",
  );
  assert.equal(
    scenarioPosts("creation-normal")[1].path,
    `/v1/command-intents/${creationIntentID}/execute`,
  );
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await page
    .locator("[data-operation-summary]")
    .getByText("等待运行", { exact: true })
    .waitFor();
  assert.equal(scenarioPosts("creation-normal").length, 2);
  assert(
    requests.some(
      (item) =>
        item.scenario === "creation-normal" &&
        item.path === `/v1/operations/${operationID}` &&
        item.method === "GET",
    ),
  );
  await check("creation-confirm-original-receipt-no-automatic-run");
  assert.equal(new URL(page.url()).hash, `#/operations/${operationID}`);
  const confirmedPosts = scenarioPosts("creation-normal").length;
  await page.reload({ waitUntil: "networkidle" });
  await page
    .locator("[data-operation-summary]")
    .getByText("等待运行", { exact: true })
    .waitFor();
  assert.equal(new URL(page.url()).hash, `#/operations/${operationID}`);
  assert.equal(
    scenarioPosts("creation-normal").length,
    confirmedPosts,
    "refresh of adopted receipt never prepares, executes or runs again",
  );
  await check("creation-original-detail-route-survives-refresh");

  await load("creation-mobile");
  await page.setViewportSize({ width: 390, height: 844 });
  await select(0, "IM 工作空间");
  await addVault();
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "no mobile document overflow",
  );
  for (const rect of await drafts.evaluateAll((elements) =>
    elements.map((element) => ({
      left: element.getBoundingClientRect().left,
      right: element.getBoundingClientRect().right,
    })),
  )) {
    assert(rect.left >= 0 && rect.right <= 390, "step cards fit mobile dialog");
  }
  const expertToggle = drafts.first().locator("details > summary");
  await expertToggle.focus();
  await expertToggle.press("Enter");
  assert.equal(
    await drafts.first().locator("details").getAttribute("open"),
    "",
  );
  assert.equal(
    await drafts
      .first()
      .getByRole("textbox", { name: "Step Key", exact: true })
      .inputValue(),
    "source-summary-1",
  );
  await expertToggle.press("Enter");
  await check("creation-mobile-keyboard-and-explicit-scope");
  await prepare();
  assert((await summary.innerText()).includes("aero-vault.usage"));
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  );
  await check("creation-mobile-readable-server-preview");
  await page.setViewportSize({ width: 1440, height: 1100 });

  for (const [scenario, reason] of [
    ["creation-action-denied", "当前动作"],
    ["creation-source-disabled", "来源"],
  ]) {
    await load(scenario);
    await select(0, "IM 工作空间");
    assert.equal(
      await page
        .getByRole("button", { name: "获取服务端预览", exact: true })
        .isDisabled(),
      true,
    );
    assert((await form.innerText()).includes(reason));
    assert.equal(scenarioPosts(scenario).length, 0);
    await check(`${scenario}-no-preparation`);
  }
  await load("creation-invalid-payload");
  await drafts.first().locator("details > summary").click();
  await drafts
    .first()
    .getByRole("textbox", { name: "Request Payload JSON", exact: true })
    .fill('{"datasets":["aero-vault.usage"]}');
  assert.equal(
    await page
      .getByRole("button", { name: "获取服务端预览", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(await drafts.first().getByRole("checkbox").isDisabled(), true);
  assert((await drafts.first().innerText()).includes("不自动修复"));
  assert.equal(scenarioPosts("creation-invalid-payload").length, 0);
  await check("creation-cross-source-expert-payload-stays-rejected");

  await load("creation-compensation");
  await select(0, "IM 工作空间");
  await drafts.first().locator("details > summary").click();
  await drafts.first().getByLabel("补偿动作", { exact: true }).click();
  await page.getByRole("option", { name: "erase", exact: true }).click();
  await prepare();
  assert((await summary.innerText()).includes("补偿：擦除所选来源数据"));
  await page
    .getByRole("textbox", { name: "输入目标账户 ID 确认", exact: true })
    .fill("确认");
  assert.equal(
    await page
      .getByRole("button", {
        name: "确认创建任务 / Operation 回执",
        exact: true,
      })
      .isDisabled(),
    true,
  );
  assert.equal(scenarioPosts("creation-compensation").length, 1);
  await confirm("01800000-0000-7000-8000-000000000002");
  await page
    .locator("[data-operation-summary]")
    .getByText("等待运行", { exact: true })
    .waitFor();
  assert.equal(scenarioPosts("creation-compensation").length, 2);
  await check("creation-destructive-compensation-exact-account-confirmation");

  await load("creation-lost-preview");
  await select(0, "IM 工作空间");
  await page
    .getByRole("button", { name: "获取服务端预览", exact: true })
    .click();
  await page.getByText("无法连接 aero-id", { exact: false }).waitFor();
  const pointerBefore = await page.evaluate(() =>
    Object.entries(sessionStorage).filter(([key]) =>
      key.startsWith("aero-id:command-previews:"),
    ),
  );
  assert(pointerBefore.length > 0);
  await load("creation-lost-preview", { clear: false });
  assert.equal(
    scenarioPosts("creation-lost-preview").length,
    1,
    "reload does not reprepare",
  );
  await page.getByRole("button", { name: "查询原意图", exact: true }).click();
  await summary.waitFor();
  assert((await summary.innerText()).includes("IM 工作空间"));
  assert.equal(scenarioPosts("creation-lost-preview").length, 1);
  await check("creation-lost-prepare-refresh-original-key-get-only");

  await load("creation-lost-execute");
  await select(0, "IM 工作空间");
  await prepare();
  await confirm();
  await page
    .getByRole("button", { name: "仅查询原意图 / 回执", exact: true })
    .waitFor({ state: "visible" });
  await page.getByText("无法连接 aero-id", { exact: false }).waitFor();
  assert.equal(scenarioPosts("creation-lost-execute").length, 2);
  await load("creation-lost-execute", { clear: false });
  await page.getByRole("button", { name: "查询原意图", exact: true }).click();
  await page
    .locator("[data-operation-summary]")
    .getByText("等待运行", { exact: true })
    .waitFor();
  assert.equal(
    scenarioPosts("creation-lost-execute").length,
    2,
    "original intent GET adopts receipt without execute retry or Run",
  );
  await check("creation-lost-execute-refresh-original-id-get-only");

  await load("creation-server-denied");
  await select(0, "IM 工作空间");
  await page
    .getByRole("button", { name: "获取服务端预览", exact: true })
    .click();
  await page
    .getByText("受控服务端拒绝预览；保留原键", { exact: false })
    .waitFor();
  await page.getByRole("button", { name: "查询原意图", exact: true }).click();
  await page
    .getByText("受控原键尚无回执；不证明在途请求不存在", { exact: false })
    .waitFor();
  assert.equal(scenarioPosts("creation-server-denied").length, 1);
  await check("creation-server-denial-and-notfound-never-replace-key");
  assert(posts().every((item) => item.original_command_validated === true));
  await save(
    "creation-validated-commands.json",
    `${JSON.stringify(facts, null, 2)}\n`,
  );
  return passed;
}
