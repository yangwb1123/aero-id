import assert from "node:assert/strict";
import { syncJobID, exportJobID } from "./job-results-data.mjs";
import { operationID } from "./operation-results-data.mjs";
import { otherOperationID } from "./task-navigation-data.mjs";

export async function runTaskNavigationScenarios({
  page,
  origin,
  requests,
  checkpoint,
  setScenario,
  save,
}) {
  let serial = 0;
  let passed = 0;
  const observations = [];
  for (const family of [
    {
      page: "jobs",
      html: "job-results",
      id: syncJobID,
      other: exportJobID,
      summary: "[data-job-summary]",
      button: "查看详情",
      back: "返回任务列表",
      close: "关闭",
      path: "/v1/sync/jobs/",
    },
    {
      page: "operations",
      html: "operation-results",
      id: operationID,
      other: otherOperationID,
      summary: "[data-operation-summary]",
      button: "查看时间线",
      back: "返回操作列表",
      close: "返回操作列表",
      path: "/v1/operations/",
    },
  ]) {
    const panel = page.locator(family.summary);
    const nav = page.locator("nav[data-task-navigation]");
    const route = `#/${family.page}/${family.id}`;
    const root = `#/${family.page}`;
    const check = async (name) => {
      observations.push({
        name,
        hash: new URL(page.url()).hash,
        requests: requests
          .filter((item) =>
            item.scenario.startsWith(`navigation-${family.page}-`),
          )
          .map(({ method, path, query }) => ({ method, path, query })),
      });
      await checkpoint(name);
      passed++;
    };
    const load = async (state, direct = false) => {
      setScenario(`navigation-${family.page}-${state}`);
      await page.goto(
        `${origin}/browser-fixtures/${family.html}.html?navigation=${++serial}${direct ? route : root}`,
        { waitUntil: "networkidle", timeout: 30000 },
      );
    };
    await load("healthy");
    assert.equal(await panel.count(), 0);
    await page
      .getByRole("button", { name: family.button, exact: true })
      .first()
      .click();
    await panel.waitFor();
    assert.equal(new URL(page.url()).hash, route);
    assert.equal(
      await page.evaluate(() =>
        document.activeElement?.getAttribute("aria-label"),
      ),
      family.page === "jobs" ? "原任务详情" : "原 Operation 详情",
      "selection moves focus to original detail",
    );
    assert(
      await panel.evaluate(
        (element) =>
          element.getBoundingClientRect().top <
          document
            .querySelector('[aria-label$="列表（可横向滚动）"]')
            .getBoundingClientRect().top,
      ),
      "selected results must precede the long list",
    );
    assert.equal(
      await nav
        .getByRole("link", {
          name:
            family.page === "jobs" ? "原任务详情链接" : "原 Operation 详情链接",
          exact: true,
        })
        .getAttribute("href"),
      route,
    );
    await check(`navigation-${family.page}-list-to-shareable-original`);

    await page.reload({ waitUntil: "networkidle" });
    await panel.waitFor();
    assert.equal(new URL(page.url()).hash, route);
    assert(requests.some((item) => item.path === family.path + family.id));
    await check(`navigation-${family.page}-refresh-original-detail-only`);
    await page.goBack({ waitUntil: "networkidle" });
    await panel.waitFor({ state: "detached" });
    assert.equal(new URL(page.url()).hash, root);
    await page.goForward({ waitUntil: "networkidle" });
    await panel.waitFor();
    assert.equal(new URL(page.url()).hash, route);
    await check(`navigation-${family.page}-browser-back-forward`);

    await page.getByRole("button", { name: family.close, exact: true }).click();
    await panel.waitFor({ state: "detached" });
    assert.equal(new URL(page.url()).hash, root);
    await page.reload({ waitUntil: "networkidle" });
    assert.equal(await panel.count(), 0);
    await check(`navigation-${family.page}-close-refresh-stays-list`);
    await page
      .getByRole("button", { name: family.button, exact: true })
      .nth(1)
      .click();
    await panel.waitFor();
    assert.equal(new URL(page.url()).hash, `#/${family.page}/${family.other}`);
    const copiedURL = page.url();
    await page.goto(
      `${origin}/browser-fixtures/${family.html}.html?copy=${++serial}#/${family.page}/${family.other}`,
      { waitUntil: "networkidle" },
    );
    await panel.waitFor();
    assert.equal(new URL(page.url()).hash, new URL(copiedURL).hash);
    await check(`navigation-${family.page}-copied-other-receipt`);

    await load("list-unavailable", true);
    await page.getByText("受控任务列表暂不可用", { exact: true }).waitFor();
    await panel.waitFor();
    await check(
      `navigation-${family.page}-original-detail-visible-during-list-outage`,
    );
    await nav.getByRole("link", { name: family.back, exact: true }).click();
    await panel.waitFor({ state: "detached" });
    assert.equal(new URL(page.url()).hash, root);
    await check(`navigation-${family.page}-list-outage-does-not-hide-detail`);
    for (const state of ["detail-denied", "detail-missing"]) {
      await load(state, true);
      await page
        .getByText(
          state === "detail-denied"
            ? "受控原详情读取被拒绝"
            : "受控原回执不存在或不可见",
          { exact: true },
        )
        .waitFor();
      assert.equal(await panel.count(), 0);
      await check(`navigation-${family.page}-${state}-navigation-visible`);
      await nav.getByRole("link", { name: family.back, exact: true }).click();
      await nav.waitFor({ state: "detached" });
      assert.equal(new URL(page.url()).hash, root);
      await check(`navigation-${family.page}-${state}-return-not-new-command`);
    }
    await load("healthy", true);
    await panel.waitFor();
    await page.setViewportSize({ width: 390, height: 844 });
    assert(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
      "navigation must fit narrow viewport",
    );
    const back = nav.getByRole("link", { name: family.back, exact: true });
    await back.focus();
    await check(`navigation-${family.page}-mobile-keyboard-return-link`);
    await back.press("Enter");
    await panel.waitFor({ state: "detached" });
    assert.equal(new URL(page.url()).hash, root);
    await page.setViewportSize({ width: 1440, height: 1100 });
  }
  const own = requests.filter((request) =>
    request.scenario.startsWith("navigation-"),
  );
  assert(own.every((request) => request.method === "GET"));
  assert(
    own.every(
      (request) => !/run|reconcile|download|command-intents/.test(request.path),
    ),
  );
  await save(
    "navigation-observations.json",
    JSON.stringify(
      {
        observations,
        methods: [...new Set(own.map((request) => request.method))],
        live_sources: "not_executed",
      },
      null,
      2,
    ),
  );
  return passed;
}
