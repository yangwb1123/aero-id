import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { connect, createServer as createSocketServer } from "node:net";
import { createServer } from "vite";
import vue from "@vitejs/plugin-vue";
import { chromium } from "playwright-core";
import {
  datasets,
  capabilityFixture,
  resourceFixture,
} from "../browser-fixtures/resources-data.mjs";
import {
  accountDatasets,
  accountSummaryFixture,
} from "../browser-fixtures/account-summary-data.mjs";
import { runAccountSummaryScenarios } from "../browser-fixtures/account-summary-scenarios.mjs";
import { jobAPIResponse } from "../browser-fixtures/job-results-data.mjs";
import { runJobResultScenarios } from "../browser-fixtures/job-results-scenarios.mjs";
import { operationAPIResponse } from "../browser-fixtures/operation-results-data.mjs";
import { runOperationResultScenarios } from "../browser-fixtures/operation-results-scenarios.mjs";
import { createOperationContinuationAPI } from "../browser-fixtures/operation-continuation-data.mjs";
import { runOperationContinuationScenarios } from "../browser-fixtures/operation-continuation-scenarios.mjs";
import { createOperationCreationAPI } from "../browser-fixtures/operation-creation-data.mjs";
import { runOperationCreationScenarios } from "../browser-fixtures/operation-creation-scenarios.mjs";
import { taskNavigationAPI } from "../browser-fixtures/task-navigation-data.mjs";
import { runTaskNavigationScenarios } from "../browser-fixtures/task-navigation-scenarios.mjs";
import { taskAuditAPI } from "../browser-fixtures/task-audit-data.mjs";
import { runTaskAuditScenarios } from "../browser-fixtures/task-audit-scenarios.mjs";
import { connectionsAPI } from "../browser-fixtures/connections-data.mjs";
import { runConnectionsScenarios } from "../browser-fixtures/connections-scenarios.mjs";
import { runSourceProjectionScenarios } from "../browser-fixtures/source-projection-scenarios.mjs";

// Isolated component/API fixture, not an OAuth or deployed-source acceptance test.
const webRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const artifacts = process.env.AERO_BROWSER_ARTIFACT_DIR
  ? resolve(process.env.AERO_BROWSER_ARTIFACT_DIR)
  : undefined;
if (artifacts) await mkdir(artifacts, { recursive: true, mode: 0o700 });
const profile = await mkdtemp(
  join(artifacts ?? tmpdir(), "aero-id-owned-chrome-"),
);
const events = [];
const requests = [];
const pageErrors = [];
let chrome;
let chromeExited = false;
let browser;
let page;
let server;
let port;
let debugPort;
const chromeMessages = [];
let failed = false;
let scenario = "healthy";
function record(event, details = {}) {
  const entry = { event, ...details };
  events.push(entry);
  process.stdout.write(`${JSON.stringify(entry)}\n`);
}
async function save(name, value) {
  if (artifacts) await writeFile(join(artifacts, name), value);
}
async function bounded(promise, milliseconds, label) {
  let timeout;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timeout = setTimeout(
          () => reject(new Error(`${label} exceeded local cleanup budget`)),
          milliseconds,
        );
      }),
    ]);
  } finally {
    clearTimeout(timeout);
  }
}
async function waitForDebugPort() {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline && !chromeExited) {
    try {
      const value = (
        await readFile(join(profile, "DevToolsActivePort"), "utf8")
      ).split("\n")[0];
      const debugPort = Number(value);
      if (Number.isInteger(debugPort) && debugPort > 0 && debugPort < 65536)
        return debugPort;
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
    }
    await delay(50);
  }
  throw new Error(
    "owned Chrome did not expose its loopback debugging endpoint",
  );
}
function processAlive(pid) {
  if (!pid) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === "ESRCH") return false;
    throw error;
  }
}
async function portClosed(value) {
  return new Promise((accept, reject) => {
    const socket = connect({ host: "127.0.0.1", port: value });
    socket.setTimeout(1000);
    socket.once("connect", () => {
      socket.destroy();
      accept(false);
    });
    socket.once("error", (error) => {
      socket.destroy();
      error.code === "ECONNREFUSED" ? accept(true) : reject(error);
    });
    socket.once("timeout", () => {
      socket.destroy();
      reject(new Error("owned Vite port close check timed out"));
    });
  });
}
async function reserveLocalPort() {
  const reservation = createSocketServer();
  await new Promise((accept, reject) => {
    reservation.once("error", reject);
    reservation.listen(0, "127.0.0.1", accept);
  });
  const selected = reservation.address().port;
  await new Promise((accept, reject) =>
    reservation.close((error) => (error ? reject(error) : accept())),
  );
  record("owned_port_reservation_closed", {
    node_pid: process.pid,
    host: "127.0.0.1",
    port: selected,
  });
  return selected;
}
try {
  // Vite treats configured zero as its default port. Reserve an ephemeral port,
  // release our exact socket, then fail closed on any intervening bind race.
  const selectedPort = await reserveLocalPort();
  server = await createServer({
    root: webRoot,
    configFile: false,
    plugins: [vue()],
    server: {
      host: "127.0.0.1",
      port: selectedPort,
      strictPort: true,
      fs: { strict: true, allow: [webRoot] },
    },
  });
  await server.listen();
  const address = server.httpServer.address();
  assert.equal(address.address, "127.0.0.1");
  port = address.port;
  const origin = `http://127.0.0.1:${port}`;
  const chromeVersion = await new Promise((accept, reject) => {
    const child = spawn("/usr/bin/google-chrome", ["--version"], {
      stdio: ["ignore", "pipe", "pipe"],
    });
    record("owned_version_probe", { pid: child.pid });
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("owned Chrome version probe timed out"));
    }, 5000);
    child.once("exit", () => clearTimeout(timeout));
    let value = "";
    child.stdout.on("data", (chunk) => {
      value += chunk;
    });
    child.once("error", reject);
    child.once("exit", (code) =>
      code === 0
        ? accept(value.trim())
        : reject(new Error("system Chrome version probe failed")),
    );
  });
  chrome = spawn(
    "/usr/bin/google-chrome",
    [
      "--headless=new",
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--remote-debugging-address=127.0.0.1",
      "--remote-debugging-port=0",
      `--user-data-dir=${profile}`,
      "about:blank",
    ],
    { stdio: ["ignore", "ignore", "pipe"] },
  );
  chrome.once("exit", () => {
    chromeExited = true;
  });
  chrome.stderr.on("data", (chunk) => {
    if (chromeMessages.length < 256) chromeMessages.push(String(chunk));
  });
  record("ownership", {
    node_pid: process.pid,
    chrome_pid: chrome.pid,
    profile,
    vite_host: address.address,
    vite_port: port,
    chrome_version: chromeVersion,
    iris: "published package, no stubs",
    authentication: "not tested; ephemeral non-JWT fixture marker only",
  });
  chrome.once("error", (error) =>
    pageErrors.push(`owned Chrome launch error: ${error.message}`),
  );
  debugPort = await waitForDebugPort();
  record("owned_debug_endpoint", { host: "127.0.0.1", port: debugPort });
  browser = await chromium.connectOverCDP(`http://127.0.0.1:${debugPort}`, {
    timeout: 15000,
  });
  const context = browser.contexts()[0];
  const continuationAPI = createOperationContinuationAPI();
  const creationAPI = createOperationCreationAPI();
  await context.route("**/*", async (route) => {
    try {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== origin) {
        pageErrors.push("unexpected non-fixture network request");
        await route.abort();
        return;
      }
      if (!url.pathname.startsWith("/v1/")) {
        await route.continue();
        return;
      }
      const observed = {
        scenario,
        method: request.method(),
        path: url.pathname,
        query: url.search,
      };
      requests.push(observed);
      if (scenario.startsWith("continuation-")) {
        const response = await continuationAPI(url, scenario, request);
        if (request.method() === "POST")
          observed.original_command_validated = true;
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      if (scenario.startsWith("creation-")) {
        const response = await creationAPI.response(url, scenario, request);
        if (request.method() === "POST")
          observed.original_command_validated = true;
        if (response.abort) {
          await route.abort();
          return;
        }
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      assert.equal(
        request.method(),
        "GET",
        "browser fixture must never issue a command",
      );
      if (scenario.startsWith("connections-")) {
        const response = connectionsAPI(url, scenario);
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      if (scenario.startsWith("auditnav-")) {
        const response = taskAuditAPI(url, scenario);
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      if (scenario.startsWith("navigation-")) {
        const response = taskNavigationAPI(url, scenario);
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      if (scenario.startsWith("operation-")) {
        const response = operationAPIResponse(url, scenario);
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body: JSON.stringify(
            response.error
              ? { error: response.error }
              : { data: response.data },
          ),
        });
        return;
      }
      if (scenario.startsWith("job-")) {
        const response = jobAPIResponse(url, scenario);
        await route.fulfill({
          status: response.status ?? 200,
          contentType: "application/json",
          body:
            response.artifact ??
            JSON.stringify(
              response.error
                ? { error: response.error }
                : { data: response.data },
            ),
        });
        return;
      }
      let data;
      if (url.pathname === "/v1/account-datasets")
        data = {
          datasets: scenario.startsWith("account-")
            ? accountDatasets
            : datasets,
        };
      else if (url.pathname === "/v1/me/capabilities")
        data = capabilityFixture(scenario.replace(/^account-/, ""));
      else if (url.pathname === "/v1/me")
        data = {
          account: accountSummaryFixture(scenario).account,
          sources: [],
          memberships: [],
        };
      else if (url.pathname === "/v1/me/overview") {
        assert.equal(url.searchParams.get("consistency"), "eventual");
        data = accountSummaryFixture(scenario);
      } else if (url.pathname === "/v1/me/data") {
        const selected = url.searchParams.getAll("dataset");
        assert(
          selected.every((name) =>
            accountDatasets.some((item) => item.name === name),
          ),
        );
        if (scenario === "account-read-failure") {
          await route.fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({
              error: { code: "source.unavailable", message: "受控读取故障" },
            }),
          });
          return;
        }
        data = accountSummaryFixture(scenario, selected);
        data.consistency = url.searchParams.get("consistency") ?? "eventual";
      } else if (url.pathname === "/v1/me/resources") {
        assert.equal(url.searchParams.get("consistency"), "eventual");
        data = scenario.startsWith("account-")
          ? accountSummaryFixture(scenario)
          : resourceFixture(scenario);
      } else throw new Error("unexpected API path in isolated browser fixture");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data }),
      });
    } catch (error) {
      pageErrors.push(`fixture request rejected: ${error.message}`);
      await route.abort().catch(() => {});
    }
  });
  page = await context.newPage();
  page.setDefaultTimeout(10000);
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.setViewportSize({ width: 1440, height: 1100 });
  const fixtureURL = `${origin}/browser-fixtures/resources.html`;
  const card = (dataset) =>
    page.locator(`[data-resource-dataset="${dataset}"]`);
  async function load(name) {
    scenario = name;
    await page.goto(fixtureURL, { waitUntil: "networkidle", timeout: 30000 });
    await page
      .getByRole("heading", { name: "资源摘要", exact: true })
      .waitFor();
  }
  async function checkpoint(name) {
    const text = await page.locator("body").innerText();
    assert(!text.includes("UNAPPROVED-FIELD-MUST-NOT-RENDER"));
    await save(`${name}.dom.html`, await page.content());
    if (artifacts)
      await page.screenshot({
        path: join(artifacts, `${name}.png`),
        fullPage: true,
      });
    record("scenario_passed", {
      name,
      method: "browser_fixture",
      requests: requests.filter((item) => item.scenario === scenario).length,
    });
  }
  await load("healthy");
  assert((await card("aero-vault.usage").innerText()).includes("1,048,576 B"));
  assert((await card("aero-vault.usage").innerText()).includes("12 个对象"));
  assert(
    (await card("aero-vault.buckets").innerText()).includes("design-assets"),
  );
  assert(
    (await card("aero-im.workspaces").innerText()).includes("workspace-demo"),
  );
  assert((await card("aero-im.workspaces").innerText()).includes("设计协作"));
  await checkpoint("healthy-desktop");
  await page.setViewportSize({ width: 390, height: 844 });
  assert(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    "resource layout must not overflow the mobile viewport",
  );
  const diagnosticToggle = page.getByText("数据集目录与读取边界", {
    exact: true,
  });
  await diagnosticToggle.focus();
  await diagnosticToggle.press("Enter");
  assert.equal(await page.locator("details").getAttribute("open"), "");
  await checkpoint("healthy-mobile");
  await page.setViewportSize({ width: 1440, height: 1100 });
  scenario = "empty";
  const beforeRefresh = requests.length;
  await page.getByRole("button", { name: "刷新资源摘要", exact: true }).click();
  await card("aero-vault.usage").getByText("0 B", { exact: true }).waitFor();
  const emptyUsage = await card("aero-vault.usage").innerText();
  assert(emptyUsage.includes("来源未提供") && !emptyUsage.includes("0 个对象"));
  assert(
    (await card("aero-vault.buckets").innerText()).includes(
      "本次投影未列出桶引用",
    ),
  );
  assert.equal(
    requests.length - beforeRefresh,
    2,
    "explicit refresh only repeats the two original metadata GETs",
  );
  await checkpoint("explicit-refresh-empty");
  await load("missing");
  assert((await card("aero-vault.usage").innerText()).includes("待同步"));
  assert((await card("aero-im.workspaces").innerText()).includes("未接通"));
  assert(!(await page.locator("body").innerText()).includes("0 B"));
  await checkpoint("missing-not-zero");
  await load("partial");
  assert((await card("aero-vault.usage").innerText()).includes("陈旧投影"));
  assert.equal(await card("aero-vault.buckets").locator("li").count(), 50);
  const truncatedBuckets = await card("aero-vault.buckets").innerText();
  assert(truncatedBuckets.includes("来源投影已截断"));
  assert(truncatedBuckets.includes("部分投影（已截断）"));
  assert(
    truncatedBuckets.indexOf("来源投影已截断") <
      truncatedBuckets.indexOf("bucket-00"),
    "truncation warning must precede the long reference list",
  );
  assert(
    (await card("aero-im.workspaces").innerText()).includes("未关联 IM 账户"),
  );
  await checkpoint("partial-stale-truncated");
  await load("malformed");
  assert(
    (await card("aero-vault.usage").innerText()).includes("字段无效（未展示）"),
  );
  assert(
    (await card("aero-vault.buckets").innerText()).includes("引用列表字段无效"),
  );
  assert(
    (await card("aero-im.workspaces").innerText()).includes("摘要字段无效"),
  );
  await checkpoint("malformed-not-empty");
  await load("fault");
  assert((await card("aero-vault.usage").innerText()).includes("来源故障"));
  assert((await card("aero-im.workspaces").innerText()).includes("读取被拒绝"));
  await checkpoint("fault-versus-denial");
  await load("unknown");
  assert(
    (await card("aero-vault.usage").innerText()).includes("本地读取资格：未知"),
  );
  await checkpoint("unknown-eligibility");
  await load("denied");
  assert((await card("aero-vault.usage").innerText()).includes("读取被拒绝"));
  assert(!(await page.locator("body").innerText()).includes("1,048,576 B"));
  await checkpoint("denied-eligibility");
  const accountScenarios = await runAccountSummaryScenarios({
    page,
    origin,
    requests,
    checkpoint,
    setScenario: (name) => {
      scenario = name;
    },
  });
  const jobScenarios = await runJobResultScenarios({
    page,
    origin,
    requests,
    checkpoint,
    save,
    setScenario: (name) => {
      scenario = name;
    },
  });
  const operationScenarios = await runOperationResultScenarios({
    page,
    origin,
    requests,
    checkpoint,
    setScenario: (name) => {
      scenario = name;
    },
  });
  const navigationScenarios = await runTaskNavigationScenarios({
    page,
    origin,
    requests,
    checkpoint,
    save,
    setScenario: (name) => {
      scenario = name;
    },
  });
  const auditNavigationScenarios = await runTaskAuditScenarios({
    page,
    origin,
    requests,
    checkpoint,
    save,
    setScenario: (name) => {
      scenario = name;
    },
  });
  const connectionScenarios = await runConnectionsScenarios({
    page,
    origin,
    requests,
    checkpoint,
    save,
    setScenario: (name) => {
      scenario = name;
    },
  });
  assert.deepEqual(pageErrors, []);
  assert(requests.every((request) => request.method === "GET"));
  const sourceProjectionScenarios = await runSourceProjectionScenarios({
    page,
    origin,
    requests,
    checkpoint,
    setScenario: (name) => {
      scenario = name;
    },
  });
  assert.deepEqual(pageErrors, []);
  assert(requests.every((request) => request.method === "GET"));
  const continuationScenarios = await runOperationContinuationScenarios({
    page,
    origin,
    requests,
    checkpoint,
    setScenario: (name) => {
      scenario = name;
    },
  });
  assert.deepEqual(pageErrors, []);
  const creationScenarios = await runOperationCreationScenarios({
    page,
    origin,
    requests,
    checkpoint,
    save,
    facts: creationAPI.facts,
    setScenario: (name) => {
      scenario = name;
    },
  });
  assert.deepEqual(pageErrors, []);
  assert(
    requests
      .filter((request) => request.method !== "GET")
      .every((request) => request.original_command_validated === true),
  );
  record("result", {
    status: "passed",
    scenarios:
      9 +
      accountScenarios +
      jobScenarios +
      operationScenarios +
      navigationScenarios +
      auditNavigationScenarios +
      connectionScenarios +
      sourceProjectionScenarios +
      continuationScenarios +
      creationScenarios,
    evidence_layer: "controlled browser fixture",
    live_oidc_and_sources:
      "not_executed: no deployment or source credentials supplied",
  });
} catch (error) {
  failed = true;
  record("result", { status: "failed", error: error.message });
  if (page && artifacts) {
    // Failure-only bounded bootstrap diagnostics; no headers, body or credentials.
    const bootstrap = await page
      .evaluate(
        (localOrigin) => ({
          url: location.href,
          ready_state: document.readyState,
          root_children: document.querySelector("#root")?.childElementCount,
          resources: performance
            .getEntriesByType("resource")
            .filter((entry) => new URL(entry.name).origin === localOrigin)
            .slice(-32)
            .map((entry) => ({
              path: new URL(entry.name).pathname,
              duration_ms: Math.round(entry.duration),
              response_status: entry.responseStatus,
            })),
        }),
        origin,
      )
      .catch(() => undefined);
    await save(
      "failure.bootstrap.json",
      `${JSON.stringify(bootstrap ?? null, null, 2)}\n`,
    ).catch(() => {});
    await save("failure.dom.html", await page.content()).catch(() => {});
    await page
      .screenshot({ path: join(artifacts, "failure.png"), fullPage: true })
      .catch(() => {});
  }
} finally {
  async function cleanupStep(label, action) {
    try {
      await action();
      record("cleanup_step_passed", { label });
    } catch (error) {
      failed = true;
      record("cleanup_failed", { label, error: error.message });
    }
  }
  await cleanupStep("browser", async () => {
    if (browser) await bounded(browser.close(), 5000, "owned browser close");
  });
  await cleanupStep("Chrome", async () => {
    if (chrome && !chromeExited) chrome.kill("SIGTERM");
    const deadline = Date.now() + 5000;
    while (chrome && !chromeExited && Date.now() < deadline) await delay(50);
    if (chrome && !chromeExited) {
      chrome.kill("SIGKILL");
      await bounded(
        new Promise((accept) => chrome.once("exit", accept)),
        3000,
        "owned Chrome exit",
      );
    }
    assert.equal(
      processAlive(chrome?.pid),
      false,
      "owned Chrome PID must no longer exist",
    );
    if (debugPort)
      assert.equal(
        await portClosed(debugPort),
        true,
        "owned Chrome debug port must be closed",
      );
  });
  await cleanupStep("Vite", async () => {
    if (server) await bounded(server.close(), 5000, "owned Vite close");
    if (port)
      assert.equal(
        await portClosed(port),
        true,
        "owned Vite port must be closed",
      );
  });
  await cleanupStep("temporary profile", async () => {
    assert.equal(processAlive(chrome?.pid), false);
    // Only this mkdtemp-owned profile, after Chrome exit; transient cache-file
    // removal races get three bounded filesystem retries, never force/ignore.
    await rm(profile, {
      recursive: true,
      force: false,
      maxRetries: 3,
      retryDelay: 50,
    });
  });
  record("cleanup", {
    chrome_pid: chrome?.pid,
    chrome_absent: !processAlive(chrome?.pid),
    vite_port: port,
    debug_port: debugPort,
    cleanup_status: failed ? "inspect individual events" : "passed",
  });
  await save("chrome.stderr.log", chromeMessages.join(""));
  await save("requests.json", `${JSON.stringify(requests, null, 2)}\n`);
  await save(
    "events.json",
    `${JSON.stringify({ events, pageErrors }, null, 2)}\n`,
  );
  process.exitCode = failed ? 1 : 0;
}
