import assert from "node:assert/strict";
import {
  auditCursor,
  operationID,
  operationRequestID,
} from "./operation-results-data.mjs";
import { checkOperationReadRecovery } from "./operation-read-recovery.mjs";

export async function runOperationResultScenarios({
  page,
  origin,
  requests,
  checkpoint,
  setScenario,
}) {
  const panel = page.locator("[data-operation-summary]");
  let serial = 0;
  let completed = 0;
  const auditRequests = () =>
    requests.filter(
      (request) => request.path === `/v1/audit/operations/${operationID}`,
    );
  async function check(name) {
    await checkpoint(name);
    completed++;
  }
  async function load(name, direct = true) {
    setScenario(name);
    await page.goto(
      `${origin}/browser-fixtures/operation-results.html?case=${++serial}#/operations${direct ? `/${operationID}` : ""}`,
      { waitUntil: "networkidle", timeout: 30000 },
    );
    await page
      .getByRole("heading", { name: "跨系统操作", exact: true })
      .waitFor();
    if (direct) await panel.waitFor();
  }
  await load("operation-partial", false);
  assert.equal(
    await panel.count(),
    0,
    "list does not select/run a receipt automatically",
  );
  await page.getByRole("button", { name: "查看时间线", exact: true }).click();
  await panel
    .getByRole("heading", { name: "来源同步 · 部分完成", exact: true })
    .waitFor();
  assert.deepEqual(
    await panel
      .locator("[data-operation-step]")
      .evaluateAll((elements) =>
        elements.map((element) => element.getAttribute("data-operation-step")),
      ),
    ["vault-summary", "im-summary", "later-summary"],
  );
  const partial = await panel.innerText();
  for (const text of [
    "所请求摘要不完整",
    "Vault 桶引用",
    "摘要投影条数",
    "步骤失败",
    "已跳过",
    "im-original-operation",
  ])
    assert(partial.includes(text));
  assert(!partial.includes("100%") && !partial.includes("全部成功"));
  const diagnostics = page.locator("details[data-operation-diagnostics]");
  assert.equal(await diagnostics.getAttribute("open"), null);
  assert.equal(
    await page.locator("pre.json-preview").first().isVisible(),
    false,
  );
  assert(
    await panel.evaluate(
      (element) =>
        element
          .querySelector("[data-operation-actions]")
          .getBoundingClientRect().top <
        element.querySelector("[data-operation-step]").getBoundingClientRect()
          .top,
    ),
    "original refresh/return/actions must precede the long step list",
  );
  await check("operation-list-to-readable-partial-results");
  await page.getByRole("button", { name: "返回操作列表", exact: true }).click();
  assert.equal(await panel.count(), 0);
  assert.equal(
    await page.getByRole("button", { name: "查看时间线", exact: true }).count(),
    1,
  );
  await check("operation-return-to-original-list");

  await load("operation-compensation-unknown");
  const unknown = await panel.innerText();
  for (const text of [
    "结果未知",
    "补偿结果未知",
    "forward-original",
    "compensation-original",
    "不代表整体回滚",
    "含危险来源动作或补偿",
  ])
    assert(unknown.includes(text));
  assert.equal(
    await page
      .getByRole("button", { name: "对账未知结果", exact: true })
      .isDisabled(),
    true,
    "legacy receipt lacks explicit per-step scope required by admission",
  );
  const beforeRefresh = requests.length;
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await panel.waitFor();
  assert.equal(
    requests.length - beforeRefresh,
    1,
    "refresh only reads original receipt, not audit/run/reconcile",
  );
  assert.equal(requests.at(-1).path, `/v1/operations/${operationID}`);
  await check("operation-unknown-compensation-original-get-only");

  await load("operation-compensation-success");
  assert((await panel.innerText()).includes("操作失败"));
  assert((await panel.innerText()).includes("补偿 · 步骤成功"));
  assert((await panel.innerText()).includes("不代表整体回滚"));
  await check("operation-successful-compensation-is-not-global-rollback");
  await load("operation-compensation-pending");
  assert((await panel.innerText()).includes("正在补偿"));
  assert((await panel.innerText()).includes("补偿 · 等待处理"));
  assert((await panel.innerText()).includes("计划不代表已执行"));
  await check("operation-pending-compensation-is-only-recorded-state");

  await load("operation-completed-missing");
  const missing = await panel.innerText();
  for (const text of [
    "来源同步 · 已完成",
    "未报告结果摘要",
    "未报告补偿状态",
    "来源未提供",
    "不证明当前投影新鲜",
  ])
    assert(missing.includes(text));
  await check("operation-completed-missing-fields-not-zero-or-unexecuted");
  await load("operation-unknown-forward");
  assert((await panel.innerText()).includes("步骤结果未知"));
  assert((await panel.innerText()).includes("保留原 Operation"));
  assert((await panel.innerText()).includes("来源未提供"));
  await check("operation-unknown-forward-without-source-id");

  await load("operation-invalid-steps");
  assert((await panel.innerText()).includes("步骤记录无法安全展示"));
  assert.equal(await panel.locator("[data-operation-step]").count(), 0);
  await check("operation-invalid-steps-not-empty-success");
  await load("operation-malformed");
  const malformed = await panel.innerText();
  assert(
    malformed.includes("字段无效（未展示）") &&
      malformed.includes("步骤状态未报告或无效"),
  );
  assert(!malformed.includes("private.scope"));
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "operation cards must fit narrow viewport",
  );
  const listRegion = page.getByRole("region", {
    name: "操作列表（可横向滚动）",
    exact: true,
  });
  assert(
    await listRegion.evaluate(
      (element) => element.scrollWidth > element.clientWidth,
    ),
  );
  await listRegion.focus();
  await listRegion.press("ArrowRight");
  await page.waitForFunction(
    () =>
      document.querySelector('[aria-label="操作列表（可横向滚动）"]')
        .scrollLeft > 0,
  );
  await check("operation-mobile-readable-steps-and-list");
  await page.locator("details[data-operation-diagnostics] > summary").focus();
  await page
    .locator("details[data-operation-diagnostics] > summary")
    .press("Enter");
  const region = page.getByRole("region", {
    name: "步骤技术元数据（可横向滚动）",
    exact: true,
  });
  await region.focus();
  await region.press("ArrowRight");
  await page.waitForFunction(
    () =>
      document.querySelector('[aria-label="步骤技术元数据（可横向滚动）"]')
        .scrollLeft > 0,
  );
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  );
  await check("operation-mobile-keyboard-diagnostics");
  await page.setViewportSize({ width: 1440, height: 1100 });

  await load("operation-requested");
  assert((await panel.innerText()).includes("本详情未提供逐步骤数据集范围"));
  assert.equal(
    await page
      .getByRole("button", { name: "运行 Operation", exact: true })
      .isDisabled(),
    true,
  );
  await check("operation-legacy-missing-scope-keeps-run-paused");

  // A requested receipt can legitimately poll before the next manual action.
  const beforePollingFailure = requests.length;
  setScenario("operation-read-failure");
  await page.getByRole("button", { name: "重试", exact: true }).waitFor();
  assert.equal(await panel.count(), 0);
  assert.equal(requests.length - beforePollingFailure, 1);
  assert.equal(requests.at(-1).path, `/v1/operations/${operationID}`);
  assert.equal(requests.at(-1).method, "GET");
  await check("operation-background-get-failure-before-manual-recovery");

  await checkOperationReadRecovery({
    page,
    panel,
    requests,
    setScenario,
    load,
  });
  await check("operation-failed-read-explicit-original-get-recovery");

  await load("operation-audit-denied");
  await page.getByText("受控审计读取被拒绝", { exact: true }).waitFor();
  assert.equal(
    await page.getByText("本次查询无可见审计事实", { exact: true }).count(),
    0,
  );
  const beforeAuditRetry = auditRequests().length;
  setScenario("operation-audit-empty");
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await page.getByText("本次查询无可见审计事实", { exact: true }).waitFor();
  assert.equal(auditRequests().length, beforeAuditRetry + 1);
  await check("operation-audit-denial-is-not-empty-explicit-get-retry");

  await load("operation-partial");
  await page.getByText(/步骤结果已记录/).waitFor();
  assert(
    (await page.locator("body").innerText()).includes(
      "fact-original-source-id",
    ),
  );
  setScenario("operation-audit-later-failure");
  await page.getByRole("button", { name: "加载更早事实", exact: true }).click();
  await page.getByText("受控后续审计读取故障", { exact: true }).waitFor();
  assert(
    (await page.locator("body").innerText()).includes(
      "fact-original-source-id",
    ),
  );
  const cursorOnFailure = new URLSearchParams(auditRequests().at(-1).query).get(
    "cursor",
  );
  assert.equal(cursorOnFailure, auditCursor);
  setScenario("operation-audit-recovered");
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await page.getByText(/原操作已创建/).waitFor();
  assert.equal(
    new URLSearchParams(auditRequests().at(-1).query).get("cursor"),
    auditCursor,
  );
  assert(
    (await page.locator("body").innerText()).includes(
      "Vault 用量、Vault 桶引用",
    ),
  );
  await check("operation-audit-source-facts-original-cursor-recovery");

  await panel.getByRole("link", { name: "打开审计台账", exact: true }).click();
  await page.getByRole("heading", { name: "审计台账", exact: true }).waitFor();
  await page
    .getByRole("textbox", { name: "Operation ID", exact: true })
    .fill(operationID);
  await page
    .getByRole("textbox", { name: "Request ID", exact: true })
    .fill(operationRequestID);
  await page.getByRole("button", { name: "查询", exact: true }).click();
  await page.getByText("没有符合条件的审计事实", { exact: true }).waitFor();
  assert.equal(
    new URLSearchParams(requests.at(-1).query).get("operation_id"),
    operationID,
  );
  assert.equal(
    new URLSearchParams(requests.at(-1).query).get("request_id"),
    operationRequestID,
  );
  await check("operation-to-audit-exact-original-id-filter");
  await load("operation-partial");
  await panel
    .getByRole("link", { name: "查看当前资源摘要", exact: true })
    .click();
  await page.getByRole("heading", { name: "资源摘要", exact: true }).waitFor();
  assert(
    (
      await page
        .locator('[data-resource-dataset="aero-vault.usage"]')
        .innerText()
    ).includes("1,048,576 B"),
  );
  assert.equal(requests.at(-1).path, "/v1/me/resources");
  await check("operation-to-current-resources-not-historical-snapshot");
  assert(
    requests
      .filter((request) => request.scenario.startsWith("operation-"))
      .every((request) => request.method === "GET"),
  );
  return completed;
}
