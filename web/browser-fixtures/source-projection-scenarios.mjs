import assert from "node:assert/strict";

// Original ConnectionsPage + AccountDataPanel/client, published Iris; no live identity.
export async function runSourceProjectionScenarios({
  page,
  origin,
  requests,
  setScenario,
  checkpoint,
}) {
  const sources = page.locator('[data-reference-list="sources"]');
  const browser = sources.locator("[data-source-data-browser]");
  const selection = () => browser.getByLabel("数据集", { exact: true });
  const dataReads = () =>
    requests.filter((item) => item.path === "/v1/me/data");
  let count = 0;
  async function observed(name) {
    await checkpoint(name);
    count += 1;
  }
  async function open(name) {
    setScenario(name);
    await page.goto(
      `${origin}/browser-fixtures/account-summary.html?case=${encodeURIComponent(name)}#/connections`,
      { waitUntil: "networkidle", timeout: 30000 },
    );
    await sources
      .getByRole("button", { name: "查看来源范围", exact: true })
      .click();
    const before = requests.length;
    await sources
      .getByRole("button", { name: "选择此来源的数据集", exact: true })
      .click();
    await browser.waitFor();
    return before;
  }
  async function choose(name) {
    await selection().click();
    await page
      .getByRole("option", {
        name: new RegExp(`^${name.replaceAll(".", "\\.")} ·`),
      })
      .click();
    await page.keyboard.press("Escape");
    assert((await selection().innerText()).includes(name));
  }
  async function read() {
    await browser
      .getByRole("button", { name: "读取账户数据", exact: true })
      .click();
  }
  const beforeOpen = dataReads().length;
  const beforeCatalog = await open("connections-projection-healthy");
  await selection().waitFor();
  assert.equal(requests.length, beforeCatalog + 1);
  assert.equal(requests.at(-1).path, "/v1/account-datasets");
  assert.equal(dataReads().length, beforeOpen);
  assert(
    (await browser.innerText()).includes(
      "不是按原生租户、工作空间或桶 ID 过滤",
    ),
  );
  assert(await browser.evaluate((node) => document.activeElement === node));
  await selection().click();
  const options = await page.getByRole("option").allTextContents();
  assert.equal(options.length, 2);
  assert(options.every((option) => option.startsWith("aero-vault.")));
  await page.keyboard.press("Escape");
  await observed("source-projection-reference-to-same-source-selection");
  await read();
  await browser
    .getByText("请显式选择至少一个同源数据集；不会默认读取全部范围", {
      exact: true,
    })
    .waitFor();
  assert.equal(dataReads().length, beforeOpen);
  await observed("source-projection-empty-selection-never-reads-all");
  await choose("aero-vault.usage");
  assert.equal(dataReads().length, beforeOpen);
  setScenario("connections-projection-partial");
  await read();
  await browser.getByText("1,048,576 B", { exact: true }).waitFor();
  assert.equal(dataReads().length, beforeOpen + 1);
  assert.deepEqual(
    [...new URLSearchParams(dataReads().at(-1).query)],
    [
      ["dataset", "aero-vault.usage"],
      ["consistency", "eventual"],
    ],
  );
  assert.equal(await browser.locator("[data-resource-dataset]").count(), 1);
  assert((await browser.innerText()).includes("陈旧"));
  assert((await browser.innerText()).includes("聚合结果不完整"));
  await observed("source-projection-explicit-local-read-and-partial-result");
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await observed("source-projection-selected-result-mobile");
  const beforeReturn = requests.length;
  const back = browser.getByRole("button", {
    name: "返回来源范围",
    exact: true,
  });
  await back.focus();
  await back.press("Enter");
  const detail = sources.locator("[data-reference-details]");
  await detail.getByText("same-opaque-reference", { exact: true }).waitFor();
  assert(await detail.evaluate((node) => document.activeElement === node));
  assert((await detail.innerText()).includes("vault-native-account"));
  assert.equal(requests.length, beforeReturn);
  await sources
    .getByRole("button", { name: "返回来源列表", exact: true })
    .press("Enter");
  await detail.waitFor({ state: "detached" });
  assert.equal(requests.length, beforeReturn);
  await observed("source-projection-keyboard-return-to-original-reference");
  await page.setViewportSize({ width: 1440, height: 1100 });

  await open("connections-projection-read-error");
  await choose("aero-vault.usage");
  const beforeError = dataReads().length;
  await read();
  await browser.getByText("受控投影读取故障", { exact: true }).waitFor();
  assert.equal(dataReads().length, beforeError + 1);
  const originalQuery = dataReads().at(-1).query;
  assert((await selection().innerText()).includes("aero-vault.usage"));
  setScenario("connections-projection-healthy");
  await read();
  await browser.getByText("1,048,576 B", { exact: true }).waitFor();
  assert.equal(dataReads().length, beforeError + 2);
  assert.equal(dataReads().at(-1).query, originalQuery);
  await observed(
    "source-projection-read-failure-explicit-original-selection-retry",
  );

  await open("connections-projection-catalog-error");
  await browser.getByText("受控目录读取故障", { exact: true }).waitFor();
  const failedCatalog = requests.length;
  const beforeCatalogRetry = dataReads().length;
  setScenario("connections-projection-healthy");
  await browser.getByRole("button", { name: "重试", exact: true }).click();
  await selection().waitFor();
  assert.equal(requests.length, failedCatalog + 1);
  assert.equal(requests.at(-1).path, "/v1/account-datasets");
  assert.equal(dataReads().length, beforeCatalogRetry);
  await observed("source-projection-catalog-failure-explicit-retry-only");

  for (const state of ["denied", "unknown"]) {
    const before = dataReads().length;
    await open(`connections-projection-${state}-dataset`);
    await browser
      .getByText("没有已核实可读的同源数据集", { exact: true })
      .waitFor();
    await read();
    assert.equal(dataReads().length, before);
    await observed(`source-projection-${state}-dataset-not-a-grant`);
  }

  await open("connections-projection-im");
  await choose("aero-im.workspaces");
  await read();
  await browser.getByText("workspace-demo", { exact: true }).waitFor();
  assert((await browser.innerText()).includes("设计协作"));
  assert.equal(
    await browser.locator('[data-resource-dataset="aero-vault.usage"]').count(),
    0,
  );
  assert.deepEqual(
    [...new URLSearchParams(dataReads().at(-1).query)],
    [
      ["dataset", "aero-im.workspaces"],
      ["consistency", "eventual"],
    ],
  );
  await observed("source-projection-im-reference-isolated-from-vault");
  return count;
}
