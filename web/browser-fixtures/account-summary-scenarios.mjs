import assert from "node:assert/strict";

// Real page/components + real AeroIdClient, controlled API responses only.
export async function runAccountSummaryScenarios({
  page,
  origin,
  requests,
  setScenario,
  checkpoint,
}) {
  const fixtureURL = `${origin}/browser-fixtures/account-summary.html`;
  const card = (dataset) =>
    page.locator(`[data-resource-dataset="${dataset}"]`);
  const scope = page.locator('[aria-label="本次读取范围"]');
  const dataReads = () =>
    requests.filter((item) => item.path === "/v1/me/data");
  async function openData(scenario) {
    setScenario(scenario);
    // A hash-only navigation reuses the mounted DataPage and its selection.
    // Each independent fixture scenario needs a fresh document/report instead.
    await page.goto(
      `${fixtureURL}?case=${encodeURIComponent(scenario)}#/data`,
      {
        waitUntil: "networkidle",
        timeout: 30000,
      },
    );
    await page
      .getByRole("button", { name: "读取账户数据", exact: true })
      .waitFor();
    assert.equal(await scope.count(), 0);
    assert(
      (await page.getByLabel("数据集", { exact: true }).innerText()).includes(
        "选择一个或多个数据集",
      ),
    );
  }
  async function choose(names) {
    const selector = page.getByLabel("数据集", { exact: true });
    await selector.click();
    for (const name of names) {
      await page
        .getByRole("option", {
          name: new RegExp(`^${name.replaceAll(".", "\\.")} ·`),
        })
        .click();
    }
    await page.keyboard.press("Escape");
    for (const name of names)
      assert((await selector.innerText()).includes(name));
  }
  async function read() {
    await page
      .getByRole("button", { name: "读取账户数据", exact: true })
      .click();
    await scope.waitFor();
  }
  setScenario("account-healthy");
  await page.goto(fixtureURL, { waitUntil: "networkidle", timeout: 30000 });
  await page.getByRole("heading", { name: "账户概览", exact: true }).waitFor();
  assert((await card("aero-vault.usage").innerText()).includes("1,048,576 B"));
  assert((await card("aero-im.workspaces").innerText()).includes("设计协作"));
  assert.equal(
    dataReads().length,
    0,
    "overview must not automatically browse data",
  );
  assert(
    (
      await page.locator('[aria-label="本次投影账户范围"]').first().innerText()
    ).includes("tenant-demo"),
  );
  await checkpoint("account-overview-business");
  await page.setViewportSize({ width: 390, height: 844 });
  const technicalToggle = page.getByText("技术统计与来源新鲜度", {
    exact: true,
  });
  await technicalToggle.focus();
  await technicalToggle.press("Enter");
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "expanded overview diagnostics must stay inside the mobile viewport",
  );
  await checkpoint("account-overview-mobile");
  await technicalToggle.press("Enter");
  await page.setViewportSize({ width: 1440, height: 1100 });
  const beforeRefresh = requests.length;
  await page.getByRole("button", { name: "刷新账户概览", exact: true }).click();
  await card("aero-vault.usage")
    .getByText("1,048,576 B", { exact: true })
    .waitFor();
  assert.equal(
    requests.length - beforeRefresh,
    3,
    "explicit overview refresh repeats only its three original GETs",
  );
  await page.getByRole("link", { name: "按范围浏览", exact: true }).click();
  await page
    .getByRole("heading", { name: "账户数据浏览", exact: true })
    .waitFor();
  assert((await page.locator("body").innerText()).includes("尚未读取"));
  assert.equal(dataReads().length, 0);
  await choose(["aero-vault.usage", "aero-im.workspaces"]);
  assert.equal(
    dataReads().length,
    0,
    "selecting scope alone must not submit GET or command",
  );
  await read();
  assert.equal(await page.locator("[data-resource-dataset]").count(), 2);
  assert.equal(await card("aero-vault.buckets").count(), 0);
  assert((await card("aero-vault.usage").innerText()).includes("1,048,576 B"));
  assert(
    (await card("aero-im.workspaces").innerText()).includes("workspace-demo"),
  );
  const selectedQuery = new URLSearchParams(dataReads().at(-1).query);
  assert.deepEqual(selectedQuery.getAll("dataset").sort(), [
    "aero-im.workspaces",
    "aero-vault.usage",
  ]);
  assert.equal(selectedQuery.get("consistency"), "eventual");
  await checkpoint("account-explicit-scope");
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "selected dataset labels must wrap within the mobile viewport",
  );
  await checkpoint("account-selected-scope-mobile");
  await page.setViewportSize({ width: 1440, height: 1100 });
  const beforeClear = dataReads().length;
  await page.getByRole("button", { name: "清除选择", exact: true }).click();
  await scope.waitFor({ state: "detached" });
  assert.equal(await page.locator("[data-resource-dataset]").count(), 0);
  assert.equal(dataReads().length, beforeClear);
  await read();
  assert((await scope.innerText()).includes("全部已注册数据集"));
  assert.equal(
    new URLSearchParams(dataReads().at(-1).query).getAll("dataset").length,
    0,
  );
  assert.equal(await page.locator("[data-resource-dataset]").count(), 3);
  assert(
    (await page.locator("body").innerText()).includes("暂未提供业务字段视图"),
  );
  const diagnostic = page.getByText("本次投影元数据与读取边界", {
    exact: true,
  });
  await diagnostic.focus();
  await diagnostic.press("Enter");
  assert.equal(await page.locator("details").getAttribute("open"), "");
  assert(
    !(await page.locator("body").innerText()).includes(
      "UNAPPROVED-FIELD-MUST-NOT-RENDER",
    ),
  );
  await checkpoint("account-all-and-diagnostics");
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "data browsing must not overflow at 390px",
  );
  assert(
    await page
      .locator("details .table-scroll")
      .evaluate((element) => element.scrollWidth > element.clientWidth),
    "mobile diagnostics remain locally scrollable instead of crushing columns",
  );
  await checkpoint("account-scope-mobile");
  await page.setViewportSize({ width: 1440, height: 1100 });
  await page.getByRole("link", { name: "返回账户概览", exact: true }).click();
  await page.getByRole("heading", { name: "账户概览", exact: true }).waitFor();
  await page.getByRole("link", { name: "资源摘要", exact: true }).click();
  await page.getByRole("heading", { name: "资源摘要", exact: true }).waitFor();
  await checkpoint("account-return-and-resource-link");

  await openData("account-missing");
  await choose(["aero-vault.usage"]);
  await read();
  assert((await card("aero-vault.usage").innerText()).includes("待同步"));
  assert.equal(await card("aero-im.workspaces").count(), 0);
  assert(!(await page.locator("body").innerText()).includes("0 B"));
  await checkpoint("account-selected-missing");
  await openData("account-partial");
  await choose(["aero-vault.buckets", "aero-im.workspaces"]);
  await read();
  assert(
    (await card("aero-vault.buckets").innerText()).includes("来源投影已截断"),
  );
  assert.equal(await card("aero-vault.buckets").locator("li").count(), 50);
  assert(
    (await card("aero-im.workspaces").innerText()).includes("未关联 IM 账户"),
  );
  assert.equal(await card("aero-vault.usage").count(), 0);
  await checkpoint("account-selected-partial");
  await openData("account-empty");
  await choose(["aero-vault.usage", "aero-vault.buckets"]);
  await read();
  const usage = await card("aero-vault.usage").innerText();
  assert(
    usage.includes("0 B") &&
      usage.includes("来源未提供") &&
      !usage.includes("0 个对象"),
  );
  assert(
    (await card("aero-vault.buckets").innerText()).includes(
      "本次投影未列出桶引用",
    ),
  );
  await checkpoint("account-empty-versus-unprovided");
  await openData("account-denied");
  await choose(["aero-vault.usage"]);
  await read();
  assert((await card("aero-vault.usage").innerText()).includes("读取被拒绝"));
  assert(!(await page.locator("body").innerText()).includes("1,048,576 B"));
  await checkpoint("account-denied-local-guidance");
  await openData("account-read-failure");
  await choose(["aero-vault.usage"]);
  await page.getByRole("button", { name: "读取账户数据", exact: true }).click();
  await page.getByText("受控读取故障", { exact: true }).waitFor();
  assert.equal(await scope.count(), 0);
  const failedCount = dataReads().length;
  setScenario("account-healthy");
  await read();
  assert.equal(
    dataReads().length,
    failedCount + 1,
    "error recovery requires one explicit original GET, never a task",
  );
  assert((await card("aero-vault.usage").innerText()).includes("1,048,576 B"));
  await checkpoint("account-explicit-read-recovery");
  return 12;
}
