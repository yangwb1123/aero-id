import assert from "node:assert/strict";
import { operationID } from "./operation-results-data.mjs";
import { originalReconcileKey } from "./operation-continuation-data.mjs";

export async function runOperationContinuationScenarios({
  page,
  origin,
  requests,
  checkpoint,
  setScenario,
}) {
  const panel = page.locator("[data-operation-summary]");
  const posts = () =>
    requests.filter(
      (item) =>
        item.scenario.startsWith("continuation-") && item.method === "POST",
    );
  let completed = 0;
  let serial = 0;
  async function check(name) {
    await checkpoint(name);
    completed++;
  }
  async function load(name, direct = true) {
    setScenario(name);
    await page.goto(
      `${origin}/browser-fixtures/operation-results.html?case=${name}-${++serial}#/operations${direct ? `/${operationID}` : ""}`,
      { waitUntil: "networkidle", timeout: 30000 },
    );
    await page
      .getByRole("heading", { name: "跨系统操作", exact: true })
      .waitFor();
    if (direct) await panel.waitFor();
  }
  await page.addInitScript(
    ({ id, key }) => {
      if (location.search.includes("continuation-unknown"))
        sessionStorage.setItem(`aero-id:operation-reconcile:${id}`, key);
    },
    { id: operationID, key: originalReconcileKey },
  );
  await load("continuation-requested", false);
  assert.equal(posts().length, 0);
  await page.getByRole("button", { name: "查看时间线", exact: true }).click();
  await panel.waitFor();
  const ranges = await panel
    .locator("[data-operation-step-datasets]")
    .allTextContents();
  assert.equal(ranges.length, 2);
  assert(
    ranges[0].includes("IM 工作空间") &&
      ranges[0].includes("aero-im.workspaces"),
  );
  assert(
    ranges[1].includes("Vault 用量") &&
      ranges[1].includes("aero-vault.buckets"),
  );
  assert(
    (await page.locator("[data-operation-admission]").innerText()).includes(
      "可显式运行原 Operation",
    ),
  );
  assert.equal(
    await page
      .getByRole("button", { name: "运行 Operation", exact: true })
      .isDisabled(),
    false,
  );
  assert.equal(
    posts().length,
    0,
    "reading explicit scope must not run automatically",
  );
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await check("continuation-mobile-readable-per-step-scopes-before-run");
  await page.setViewportSize({ width: 1440, height: 1100 });
  await page
    .getByRole("button", { name: "运行 Operation", exact: true })
    .click();
  await panel
    .getByRole("heading", { name: "来源同步 · 已完成", exact: true })
    .waitFor();
  assert.equal(posts().length, 1);
  assert.equal(posts().at(-1).path, `/v1/operations/${operationID}/run`);
  assert.deepEqual(
    await panel.locator("[data-operation-step-datasets]").allTextContents(),
    ranges,
  );
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await panel.waitFor();
  assert.equal(posts().length, 1, "refresh must only GET the original receipt");
  await check("continuation-explicit-original-run-result-and-get-only-refresh");

  await load("continuation-unknown");
  assert((await panel.innerText()).includes("im-original-operation"));
  assert(
    (await page.locator("[data-operation-admission]").innerText()).includes(
      "可显式对账原来源结果",
    ),
  );
  const beforeReconcile = posts().length;
  await page.getByRole("button", { name: "对账未知结果", exact: true }).click();
  await panel
    .getByRole("heading", { name: "来源同步 · 已完成", exact: true })
    .waitFor();
  assert.equal(posts().length, beforeReconcile + 1);
  assert.equal(posts().at(-1).path, `/v1/operations/${operationID}/reconcile`);
  assert(
    (await panel.innerText()).includes("im-original-operation"),
    "original source operation ID survives reconciliation",
  );
  assert.equal(
    await page.evaluate(
      (id) => sessionStorage.getItem(`aero-id:operation-reconcile:${id}`),
      operationID,
    ),
    null,
  );
  await check("continuation-unknown-original-key-and-source-id-reconciliation");

  await load("continuation-dangerous");
  assert((await panel.innerText()).includes("补偿沿用本步骤同一持久范围"));
  const beforeDangerous = posts().length;
  await page
    .getByRole("button", { name: "准备运行危险 Operation", exact: true })
    .click();
  const confirm = page.getByRole("button", { name: "确认运行", exact: true });
  assert.equal(await confirm.isDisabled(), true);
  await page
    .getByRole("textbox", { name: "确认短语", exact: true })
    .fill("CONFIRM OPERATION RUN ");
  assert.equal(await confirm.isDisabled(), true);
  await page
    .getByRole("textbox", { name: "确认短语", exact: true })
    .fill("CONFIRM OPERATION RUN");
  assert.equal(
    posts().length,
    beforeDangerous,
    "correcting the phrase does not run automatically",
  );
  await confirm.click();
  await panel
    .getByRole("heading", { name: "来源同步 · 已完成", exact: true })
    .waitFor();
  assert.equal(posts().length, beforeDangerous + 1);
  await check("continuation-dangerous-compensation-keeps-exact-confirmation");

  for (const [scenario, reason] of [
    ["continuation-denied", "source.sync 已拒绝"],
    [
      "continuation-compensation-denied",
      "补偿动作所需资格 account.erase 已拒绝",
    ],
    ["continuation-disabled-source", "来源摘要未启用"],
    ["continuation-foreign-home", "主区域不是当前区域"],
    ["continuation-invalid-scope", "逐步骤明确范围或有序动作 / 补偿计划未确认"],
  ]) {
    const before = posts().length;
    await load(scenario);
    assert(
      (await page.locator("[data-operation-admission]").innerText()).includes(
        reason,
      ),
    );
    const button = page.getByRole("button", {
      name:
        scenario === "continuation-compensation-denied"
          ? "准备运行危险 Operation"
          : "运行 Operation",
      exact: true,
    });
    assert.equal(await button.isDisabled(), true);
    await page.getByRole("button", { name: "刷新状态", exact: true }).click();
    await panel.waitFor();
    assert.equal(
      posts().length,
      before,
      "denied refresh cannot issue a command",
    );
    await check(`${scenario}-reason-and-no-command`);
  }
  await load("continuation-server-denied");
  const beforeRefusal = posts().length;
  await page
    .getByRole("button", { name: "运行 Operation", exact: true })
    .click();
  await page
    .getByText("受控服务端拒绝继续原操作；请核对原回执", { exact: true })
    .waitFor();
  assert((await panel.innerText()).includes("来源同步 · 等待运行"));
  assert.equal(posts().length, beforeRefusal + 1);
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await panel.waitFor();
  assert.equal(
    posts().length,
    beforeRefusal + 1,
    "server refusal must not trigger an automatic command retry",
  );
  assert.deepEqual(
    await panel.locator("[data-operation-step-datasets]").allTextContents(),
    ranges,
  );
  await check("continuation-server-refusal-preserves-receipt-and-original-get");
  assert.equal(posts().length, 4);
  assert(
    requests
      .filter((item) => item.scenario.startsWith("continuation-"))
      .every(
        (item) =>
          item.method === "GET" ||
          [
            `/v1/operations/${operationID}/run`,
            `/v1/operations/${operationID}/reconcile`,
          ].includes(item.path),
      ),
  );
  return completed;
}
