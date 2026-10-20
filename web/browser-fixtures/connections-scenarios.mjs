import assert from "node:assert/strict";

// Published Iris + original page/client; isolated synthetic API, never live identity.
export async function runConnectionsScenarios({
  page,
  origin,
  requests,
  checkpoint,
  save,
  setScenario,
}) {
  const fixture = `${origin}/browser-fixtures/account-summary.html`;
  const sources = page.locator('[data-reference-list="sources"]');
  const members = page.locator('[data-reference-list="memberships"]');
  const sourceDetail = sources.locator("[data-reference-details]");
  const memberDetail = members.locator("[data-reference-details]");
  const observations = [];
  async function observed(name) {
    await checkpoint(name);
    observations.push({
      scenario: name,
      result: "passed",
      method: "controlled browser fixture",
    });
  }
  async function open(name, route = "connections") {
    setScenario(name);
    await page.goto(`${fixture}?case=${encodeURIComponent(name)}#/${route}`, {
      waitUntil: "networkidle",
      timeout: 30000,
    });
    await page
      .getByRole("heading", {
        name: route === "overview" ? "账户概览" : "来源与成员关系",
        exact: true,
      })
      .waitFor();
  }
  await open("connections-healthy", "overview");
  const beforeNavigation = requests.length;
  await page.getByRole("link", { name: "来源与成员关系", exact: true }).click();
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .waitFor();
  await members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .waitFor();
  assert.deepEqual(
    requests
      .slice(beforeNavigation)
      .map((item) => item.path)
      .sort(),
    ["/v1/me/memberships", "/v1/me/sources"],
  );
  assert(
    (
      await page.locator('[aria-label="本次投影账户范围"]').innerText()
    ).includes("tenant-demo"),
  );
  await observed("connections-overview-to-native-references");
  const beforeSelect = requests.length;
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .click();
  await sourceDetail.waitFor();
  const sourceText = await sourceDetail.innerText();
  for (const text of [
    "Vault 租户",
    "vault-native-account",
    "same-opaque-reference",
    "2026-10-16T00:00:00Z",
    "不是全局权限",
  ])
    assert(sourceText.includes(text));
  assert.equal(
    requests.length,
    beforeSelect,
    "selecting a returned reference must not issue N+1 requests",
  );
  assert(
    await sourceDetail.evaluate((node) => node === document.activeElement),
  );
  await observed("connections-source-scope-result");
  await sources
    .getByRole("button", { name: "返回来源列表", exact: true })
    .click();
  await sourceDetail.waitFor({ state: "detached" });
  assert(
    await sources
      .locator("[data-reference-table]")
      .evaluate((node) => node === document.activeElement),
  );
  await members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .click();
  await memberDetail.waitFor();
  const memberText = await memberDetail.innerText();
  for (const text of [
    "IM 工作空间",
    "im-native-member",
    "same-opaque-reference",
    "owner",
    "不是全局权限",
  ])
    assert(memberText.includes(text));
  assert(!memberText.includes("vault-native-account"));
  assert.equal(requests.length, beforeSelect);
  await observed("connections-member-scope-not-global-grant");
  await members
    .getByRole("button", { name: "返回成员关系列表", exact: true })
    .click();
  setScenario("connections-page-error");
  await members.getByRole("button", { name: "加载更多", exact: true }).click();
  await members.getByRole("button", { name: "重试", exact: true }).waitFor();
  assert((await members.innerText()).includes("same-opaque-reference"));
  const failedQuery = requests.at(-1).query;
  setScenario("connections-page-recovered");
  await members.getByRole("button", { name: "重试", exact: true }).click();
  await members.getByText("bucket-native-scope", { exact: true }).waitFor();
  assert.equal(
    requests.at(-1).query,
    failedQuery,
    "retry must use the original cursor",
  );
  assert.equal(
    await members
      .getByRole("button", { name: "加载更多", exact: true })
      .count(),
    0,
  );
  await members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .nth(1)
    .click();
  await memberDetail
    .getByText("vault-native-member", { exact: true })
    .waitFor();
  assert((await memberDetail.innerText()).includes("Vault 桶"));
  await observed("connections-original-cursor-error-recovery");
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await observed("connections-member-result-mobile");
  await members
    .getByRole("button", { name: "返回成员关系列表", exact: true })
    .click();
  const table = members.getByRole("region", {
    name: "成员关系引用表格，可横向滚动",
    exact: true,
  });
  await table.focus();
  await table.press("ArrowRight");
  await page.waitForFunction(
    () =>
      document.querySelector(
        '[data-reference-list="memberships"] .reference-table',
      )?.scrollLeft > 0,
  );
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  const mobileOpen = members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .first();
  await mobileOpen.focus();
  await mobileOpen.press("Enter");
  await memberDetail.getByText("im-native-member", { exact: true }).waitFor();
  assert(
    await memberDetail.evaluate((node) => node === document.activeElement),
  );
  await members
    .getByRole("button", { name: "返回成员关系列表", exact: true })
    .press("Enter");
  await memberDetail.waitFor({ state: "detached" });
  await observed("connections-reference-table-mobile-keyboard");
  await page.setViewportSize({ width: 1440, height: 1100 });
  const beforeRefresh = requests.length;
  await sources
    .getByRole("button", { name: "刷新来源账户引用", exact: true })
    .click();
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .waitFor();
  assert.equal(requests.length, beforeRefresh + 1);
  assert.equal(requests.at(-1).path, "/v1/me/sources");
  await page
    .getByRole("link", { name: "查看当前资源摘要", exact: true })
    .click();
  await page.getByRole("heading", { name: "资源摘要", exact: true }).waitFor();
  await page.goBack({ waitUntil: "networkidle" });
  await page.getByRole("link", { name: "返回账户概览", exact: true }).click();
  await page.getByRole("heading", { name: "账户概览", exact: true }).waitFor();
  await observed("connections-local-refresh-and-return-navigation");
  await open("connections-source-error");
  await sources.getByRole("button", { name: "重试", exact: true }).waitFor();
  await members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .click();
  await memberDetail.getByText("im-native-member", { exact: true }).waitFor();
  const beforeRecovery = requests.length;
  setScenario("connections-recovered");
  await sources.getByRole("button", { name: "重试", exact: true }).click();
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .waitFor();
  assert.equal(requests.length, beforeRecovery + 1);
  assert((await memberDetail.innerText()).includes("im-native-member"));
  await observed("connections-independent-list-failure-and-recovery");
  await open("connections-missing");
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .click();
  await sourceDetail.waitFor();
  assert((await sourceDetail.innerText()).includes("来源未提供"));
  assert((await sourceDetail.innerText()).includes("状态未确认"));
  assert((await sourceDetail.innerText()).includes("作用域类型未识别"));
  assert(!(await sourceDetail.innerText()).includes("Vault 租户"));
  await observed("connections-missing-not-inferred-or-zero");
  await open("connections-unavailable");
  await sources
    .getByRole("button", { name: "查看来源范围", exact: true })
    .click();
  await sourceDetail.waitFor();
  assert(
    (await sourceDetail.innerText()).includes("不可用记录（unavailable）"),
  );
  await observed("connections-unavailable-reference-keeps-warning");
  await open("connections-empty");
  assert((await sources.innerText()).includes("本次未返回来源引用"));
  assert((await members.innerText()).includes("本次未返回成员关系引用"));
  assert(
    (await page.locator("body").innerText()).includes(
      "不证明来源没有账户或成员关系",
    ),
  );
  await observed("connections-empty-query-not-global-absence");
  await open("connections-malformed");
  await sources.getByRole("button", { name: "重试", exact: true }).waitFor();
  assert(!(await sources.innerText()).includes("本次未返回来源引用"));
  await members
    .getByRole("button", { name: "查看成员范围", exact: true })
    .waitFor();
  await observed("connections-invalid-list-not-empty");
  for (const name of ["connections-denied", "connections-unknown"]) {
    const beforeDenied = requests.length;
    await open(name);
    assert.equal(
      requests
        .slice(beforeDenied)
        .filter((item) => /\/me\/(sources|memberships)$/.test(item.path))
        .length,
      0,
    );
    assert(
      (await page.locator("body").innerText()).includes("当前读取资格未确认"),
    );
    await observed(`${name}-no-reference-query`);
  }
  assert(
    requests
      .filter((item) => item.scenario.startsWith("connections-"))
      .every((item) => item.method === "GET"),
  );
  await save(
    "connections-observations.json",
    JSON.stringify(observations, null, 2),
  );
  return observations.length;
}
