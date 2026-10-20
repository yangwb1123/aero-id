import assert from "node:assert/strict";
import {
  syncJobID,
  exportJobID,
  auditJobID,
  jobRequestID,
  jobArtifact,
} from "./job-results-data.mjs";

export async function runJobResultScenarios({
  page,
  origin,
  requests,
  checkpoint,
  setScenario,
  save,
}) {
  const panel = page.locator("[data-job-summary]");
  const downloadRequests = () =>
    requests.filter((request) => request.path.endsWith("/export/download"));
  let serial = 0;
  async function load(name, id = exportJobID) {
    setScenario(name);
    await page.goto(
      `${origin}/browser-fixtures/job-results.html?case=${++serial}#/jobs/${id ?? ""}`,
      { waitUntil: "networkidle", timeout: 30000 },
    );
    await page
      .getByRole("heading", { name: "同步与导出", exact: true })
      .waitFor();
    if (id) await panel.waitFor();
  }
  await load("job-partial-sync", null);
  assert.equal(
    await panel.count(),
    0,
    "list does not automatically read any selected detail",
  );
  await page.getByRole("button", { name: "查看详情", exact: true }).click();
  await panel
    .getByRole("heading", { name: "账户同步 · 部分完成", exact: true })
    .waitFor();
  const partial = await panel.innerText();
  assert(partial.includes("Vault 用量") && partial.includes("IM 工作空间"));
  assert(partial.includes("摘要投影条数：1") && partial.includes("来源不可用"));
  assert.equal(
    await page.locator("details[data-job-diagnostics]").getAttribute("open"),
    null,
  );
  assert.equal(
    await page.locator("pre.json-preview").first().isVisible(),
    false,
  );
  await checkpoint("job-list-to-partial-results");

  await load("job-missing-source", syncJobID);
  const missing = await panel
    .locator('[data-job-source="aero-im"]')
    .innerText();
  assert(
    missing.includes("未报告来源结果") &&
      missing.includes("不能据此判断未执行"),
  );
  await checkpoint("job-missing-source-not-unexecuted");

  await load("job-export");
  assert((await panel.innerText()).includes("所选投影存在缺失或不可用"));
  assert((await panel.innerText()).includes("引用分页遍历完成"));
  assert((await panel.innerText()).includes("导出范围不完整"));
  assert((await panel.innerText()).includes("不是跨源同一时点"));
  await checkpoint("job-export-coverage-before-download");
  const originalExpiry = await panel
    .getByText("制品到期时间", { exact: true })
    .locator("..")
    .locator("dd")
    .innerText();
  const beforeDownload = downloadRequests().length;
  await page.getByRole("button", { name: "一次性下载", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  assert.equal(
    downloadRequests().length,
    beforeDownload,
    "opening confirmation does not consume",
  );
  const filePromise = page.waitForEvent("download");
  await dialog.getByRole("button", { name: "确认并下载", exact: true }).click();
  const file = await filePromise;
  assert.equal(file.suggestedFilename(), `aero-id-export-${exportJobID}.json`);
  const stream = await file.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  const content = Buffer.concat(chunks).toString("utf8");
  assert.equal(
    content,
    jobArtifact,
    "saved response must be the original fixture artifact, not regenerated client data",
  );
  await save("job-original-artifact.json", content);
  await panel.getByText("本页已接收一次性下载", { exact: true }).waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "一次性下载", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(downloadRequests().length, beforeDownload + 1);
  assert.equal(downloadRequests().at(-1).query, "?one_time=true");
  await checkpoint("job-original-one-time-download-received");

  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await panel.getByText("本页已接收一次性下载", { exact: true }).waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "一次性下载", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(
    downloadRequests().length,
    beforeDownload + 1,
    "GET refresh must not enable a second local consumption",
  );
  assert.equal(
    await panel
      .getByText("制品到期时间", { exact: true })
      .locator("..")
      .locator("dd")
      .innerText(),
    originalExpiry,
    "original metadata TTL must not change on GET refresh",
  );
  await checkpoint("job-refresh-does-not-renew-local-download");

  for (const [name, message] of [
    ["job-expired", "制品已到期"],
    ["job-disposed", "制品已消费或清理"],
    ["job-mismatch", "制品元数据不能确认"],
  ]) {
    await load(name);
    assert((await panel.innerText()).includes(message));
    assert.equal(
      await page
        .getByRole("button", { name: "一次性下载", exact: true })
        .isDisabled(),
      true,
    );
    assert.equal(downloadRequests().length, beforeDownload + 1);
    await checkpoint(name);
  }
  await load("job-unknown");
  assert((await panel.innerText()).includes("结果未知"));
  assert((await panel.innerText()).includes("只核对原制品"));
  assert.equal(
    await page.getByRole("button", { name: "一次性下载", exact: true }).count(),
    0,
  );
  const beforeRefresh = requests.length;
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await panel.waitFor();
  assert.equal(requests.length - beforeRefresh, 1);
  assert.equal(requests.at(-1).path, `/v1/sync/jobs/${exportJobID}`);
  await checkpoint("job-unknown-original-get-only");
  await load("job-failed");
  assert((await panel.innerText()).includes("任务失败"));
  assert((await panel.innerText()).includes("不代表此前来源效果已回滚"));
  assert.equal(
    await page.getByRole("button", { name: "一次性下载", exact: true }).count(),
    0,
  );
  await checkpoint("job-failed-not-unknown-or-rollback");

  await load("job-audit", auditJobID);
  const audit = await panel.innerText();
  assert(
    audit.includes("本次窗口审计事实数") && audit.includes("本次窗口已截断"),
  );
  assert(audit.includes("不是完整审计历史"));
  assert(!audit.includes("Vault 用量") && !audit.includes("缺失的数据集"));
  assert.equal(await panel.locator("dd").filter({ hasText: /^0$/ }).count(), 1);
  await checkpoint("job-audit-window-zero-and-truncation");
  await load("job-audit-denied", auditJobID);
  assert.equal(
    await page
      .getByRole("button", { name: "一次性下载", exact: true })
      .isDisabled(),
    true,
  );
  assert(
    (await panel.innerText()).includes(
      "审计导出同时需要 audit.export 与 audit.read",
    ),
  );
  await checkpoint("job-audit-current-read-permission-required");

  await load("job-malformed");
  const malformed = await panel.innerText();
  assert(
    malformed.includes("字段无效（未展示）") &&
      malformed.includes("来源未提供"),
  );
  assert(!malformed.includes("9,007,199"));
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "job results must not overflow mobile viewport",
  );
  const listRegion = page.getByRole("region", {
    name: "任务列表（可横向滚动）",
    exact: true,
  });
  assert(
    await listRegion.evaluate(
      (element) => element.scrollWidth > element.clientWidth,
    ),
    "mobile task list must retain readable columns in a local scroll region",
  );
  assert(
    await listRegion
      .locator("table")
      .evaluate((element) => element.getBoundingClientRect().width >= 850),
  );
  await listRegion.focus();
  await listRegion.press("ArrowRight");
  await page.waitForFunction(
    () =>
      document.querySelector('[aria-label="任务列表（可横向滚动）"]')
        .scrollLeft > 0,
  );
  await checkpoint("job-mobile-readable-list-and-result");
  const diagnostics = page.locator("details[data-job-diagnostics]");
  await diagnostics.locator("summary").focus();
  await diagnostics.locator("summary").press("Enter");
  assert.equal(await diagnostics.getAttribute("open"), "");
  const technicalRegion = page.getByRole("region", {
    name: "任务技术元数据（可横向滚动）",
    exact: true,
  });
  assert(
    await technicalRegion.evaluate(
      (element) => element.scrollWidth > element.clientWidth,
    ),
  );
  await technicalRegion.focus();
  await technicalRegion.press("ArrowRight");
  await page.waitForFunction(
    () =>
      document.querySelector('[aria-label="任务技术元数据（可横向滚动）"]')
        .scrollLeft > 0,
  );
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  );
  assert(
    !(await diagnostics.innerText()).includes(
      "UNAPPROVED-FIELD-MUST-NOT-RENDER",
    ),
  );
  await checkpoint("job-mobile-result-and-keyboard-diagnostics");
  await page.setViewportSize({ width: 1440, height: 1100 });

  setScenario("job-read-failure");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await page.getByRole("button", { name: "重试", exact: true }).waitFor();
  assert.equal(
    await panel.count(),
    0,
    "failed read must not keep stale result actions visible",
  );
  setScenario("job-export");
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await panel.waitFor();
  assert.equal(downloadRequests().length, beforeDownload + 1);
  await checkpoint("job-explicit-get-recovery-only");

  await panel.getByRole("link", { name: "打开审计台账", exact: true }).click();
  await page.getByRole("heading", { name: "审计台账", exact: true }).waitFor();
  await page
    .getByRole("textbox", { name: "Request ID", exact: true })
    .fill(jobRequestID);
  await page.getByRole("button", { name: "查询", exact: true }).click();
  await page.getByText("没有符合条件的审计事实", { exact: true }).waitFor();
  assert.equal(
    new URLSearchParams(requests.at(-1).query).get("request_id"),
    jobRequestID,
  );
  await checkpoint("job-result-to-audit-request-filter");
  await load("job-export");
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
  await checkpoint("job-result-to-current-resources");
  return 17;
}
