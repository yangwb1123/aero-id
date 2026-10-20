import assert from "node:assert/strict";
import { exportJobID } from "./job-results-data.mjs";
import { operationID } from "./operation-results-data.mjs";
import { auditRequestID, auditNavigationCursor } from "./task-audit-data.mjs";
export async function runTaskAuditScenarios({
  page,
  origin,
  requests,
  checkpoint,
  save,
  setScenario,
}) {
  let count = 0;
  const observations = [];
  const auditGETs = () =>
    requests.filter(
      (item) =>
        item.scenario.startsWith("auditnav-") &&
        item.path === "/v1/audit/events",
    );
  async function check(name) {
    assert(
      requests
        .filter((item) => item.scenario.startsWith("auditnav-"))
        .every((item) => item.method === "GET"),
    );
    observations.push({
      name,
      hash: new URL(page.url()).hash,
      audit_gets: auditGETs().length,
    });
    await checkpoint(name);
    count++;
  }
  async function load(scenario, operation = false) {
    setScenario(scenario);
    await page.goto(
      `${origin}/browser-fixtures/${operation ? "operation" : "job"}-results.html?audit-flow=${scenario}#/${operation ? "operations" : "jobs"}/${operation ? operationID : exportJobID}`,
      { waitUntil: "networkidle" },
    );
    await page
      .locator(operation ? "[data-operation-summary]" : "[data-job-summary]")
      .waitFor();
  }
  const context = () => page.locator("[data-task-audit-context]");
  const requestInput = () =>
    page.getByRole("textbox", { name: "Request ID", exact: true });
  const operationInput = () =>
    page.getByRole("textbox", { name: "Operation ID", exact: true });
  const query = () =>
    page.getByRole("button", { name: "查询", exact: true }).click();
  const lastQuery = () => new URLSearchParams(auditGETs().at(-1).query);

  await page.setViewportSize({ width: 1440, height: 1100 });
  await load("auditnav-job");
  const start = auditGETs().length;
  await page.locator('[data-audit-link="request"]').click();
  await context().waitFor();
  assert.equal(await requestInput().inputValue(), auditRequestID);
  assert.equal(await operationInput().inputValue(), "");
  assert.equal(
    auditGETs().length,
    start,
    "opening a filter link must not read the ledger",
  );
  await check("audit-navigation-job-prefilled-no-automatic-query");
  await query();
  await page.getByText("sync.requested", { exact: true }).waitFor();
  assert.equal(lastQuery().get("request_id"), auditRequestID);
  assert.equal(lastQuery().has("operation_id"), false);
  await requestInput().fill("edited-not-submitted");
  await page.getByRole("button", { name: "加载更多", exact: true }).click();
  await page.getByText("sync.completed", { exact: true }).waitFor();
  assert.equal(lastQuery().get("request_id"), auditRequestID);
  assert.equal(lastQuery().get("cursor"), auditNavigationCursor);
  assert(
    (await page.locator("[data-applied-audit-filter]").innerText()).includes(
      auditRequestID,
    ),
  );
  await check("audit-navigation-explicit-query-original-filter-keyset");
  await context()
    .getByRole("link", { name: "返回原任务结果", exact: true })
    .click();
  await page.locator("[data-job-summary]").waitFor();
  assert.equal(new URL(page.url()).hash, `#/jobs/${exportJobID}`);
  const beforeReload = auditGETs().length;
  await page.reload({ waitUntil: "networkidle" });
  await page.locator("[data-job-summary]").waitFor();
  assert.equal(auditGETs().length, beforeReload);
  await check("audit-navigation-return-original-job-and-refresh-only-get");

  await load("auditnav-operation", true);
  await page.locator('[data-audit-link="operation"]').click();
  await context().waitFor();
  assert.equal(await operationInput().inputValue(), operationID);
  assert.equal(await requestInput().inputValue(), "");
  const beforeOperation = auditGETs().length;
  await query();
  await page.getByText("sync.requested", { exact: true }).waitFor();
  assert.equal(auditGETs().length, beforeOperation + 1);
  assert.equal(lastQuery().get("operation_id"), operationID);
  assert.equal(lastQuery().has("request_id"), false);
  await check("audit-navigation-operation-id-not-source-or-request-id");
  await context()
    .getByRole("link", { name: "返回原操作结果", exact: true })
    .click();
  await page.locator("[data-operation-summary]").waitFor();
  await page.locator('[data-audit-link="request"]').click();
  await context().waitFor();
  assert.equal(await requestInput().inputValue(), auditRequestID);
  assert.equal(await operationInput().inputValue(), "");
  const beforeAuditReload = auditGETs().length;
  const originalHash = new URL(page.url()).hash;
  await page.reload({ waitUntil: "networkidle" });
  await context().waitFor();
  assert.equal(new URL(page.url()).hash, originalHash);
  assert.equal(await requestInput().inputValue(), auditRequestID);
  assert.equal(auditGETs().length, beforeAuditReload);
  await check("audit-navigation-refresh-restores-only-original-prefill");

  for (const suffix of ["denied", "unavailable"]) {
    await load(`auditnav-job-${suffix}`);
    await page.locator('[data-audit-link="request"]').click();
    await context().waitFor();
    await query();
    await page.getByRole("button", { name: "重试", exact: true }).waitFor();
    assert(
      (await page.locator("body").innerText()).includes("受控审计查询未获结果"),
    );
    assert.equal(await requestInput().inputValue(), auditRequestID);
    assert(
      await context()
        .getByRole("link", { name: "返回原任务结果", exact: true })
        .isVisible(),
    );
    const deniedCount = auditGETs().length;
    setScenario("auditnav-job-empty");
    await page.getByRole("button", { name: "重试", exact: true }).click();
    await page.getByText("没有符合条件的审计事实", { exact: true }).waitFor();
    assert.equal(auditGETs().length, deniedCount + 1);
    assert.equal(lastQuery().get("request_id"), auditRequestID);
    assert(
      (await page.locator("body").innerText()).includes("不证明远端未执行"),
    );
    await check(
      `audit-navigation-${suffix}-preserves-return-explicit-original-retry`,
    );
  }
  await load("auditnav-job-missing");
  assert.equal(await page.locator('[data-audit-link="request"]').count(), 0);
  assert(
    (await page.locator("[data-job-summary]").innerText()).includes(
      "不以任务 ID 或来源操作 ID 替代",
    ),
  );
  await check("audit-navigation-missing-request-id-no-invented-filter");
  await load("auditnav-job-no-read");
  await page.locator('[data-audit-link="request"]').click();
  await context().waitFor();
  const deniedRead = auditGETs().length;
  assert.equal(
    await page.getByRole("button", { name: "查询", exact: true }).isDisabled(),
    true,
  );
  assert.equal(auditGETs().length, deniedRead);
  await check("audit-navigation-current-read-guidance-denied-no-query");

  await load("auditnav-job-empty");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('[data-audit-link="request"]').focus();
  await page.locator('[data-audit-link="request"]').press("Enter");
  await context().waitFor();
  await requestInput().focus();
  await requestInput().press("Enter");
  await page.getByText("没有符合条件的审计事实", { exact: true }).waitFor();
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  );
  await check("audit-navigation-mobile-keyboard-explicit-query");
  const beforeReset = auditGETs().length;
  await page.getByRole("button", { name: "重置", exact: true }).click();
  assert.equal(await requestInput().inputValue(), "");
  assert.equal(auditGETs().length, beforeReset);
  await context()
    .getByRole("link", { name: "返回原任务结果", exact: true })
    .focus();
  await context()
    .getByRole("link", { name: "返回原任务结果", exact: true })
    .press("Enter");
  await page.locator("[data-job-summary]").waitFor();
  assert.equal(new URL(page.url()).hash, `#/jobs/${exportJobID}`);
  await check("audit-navigation-reset-does-not-query-all-keyboard-return");
  await page.setViewportSize({ width: 1440, height: 1100 });
  await save(
    "audit-navigation-observations.json",
    JSON.stringify(observations, null, 2),
  );
  return count;
}
