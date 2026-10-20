import assert from "node:assert/strict";
import { operationID } from "./operation-results-data.mjs";

// A browser-scenario driver, not a production refresh/retry implementation.
export async function checkOperationReadRecovery({
  page,
  panel,
  requests,
  setScenario,
  load,
}) {
  // requested is legitimately polled: start this manual-refresh case from a
  // separately loaded terminal receipt so a background GET cannot take its fault.
  await load("operation-partial");
  const beforeFailure = requests.length;
  setScenario("operation-read-failure");
  await page.getByRole("button", { name: "刷新状态", exact: true }).click();
  await page.getByRole("button", { name: "重试", exact: true }).waitFor();
  assert.equal(await panel.count(), 0);
  assert.equal(
    requests.length - beforeFailure,
    1,
    "explicit refresh only reads the original receipt",
  );
  assert.equal(requests.at(-1).path, `/v1/operations/${operationID}`);
  const beforeRecovery = requests.length;
  setScenario("operation-partial");
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await panel.waitFor();
  assert.equal(requests.length - beforeRecovery, 1);
  assert.equal(requests.at(-1).path, `/v1/operations/${operationID}`);
}
