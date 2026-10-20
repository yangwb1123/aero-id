import { createApp } from "vue";
import { AeroIdClient } from "../src/api/aeroId";
import { capabilityContextKey } from "../src/hooks/capabilityContext";
import { accountScopeKey } from "../src/hooks/accountContext";
import JobResultsHarness from "./JobResultsHarness.vue";
import "../src/style.css";
// Fresh in-memory non-JWT marker used only by the intercepted fixture transport.
const marker = crypto.randomUUID();
const base = new URL("/v1", location.origin).href;
const client = new AeroIdClient(base, () => marker);
const report = await client.getCapabilities();
const scope = JSON.stringify([
  base,
  report.issuer,
  report.client_id,
  report.tenant_id,
  report.account_id,
]);
const app = createApp(JobResultsHarness, { client });
app.provide(capabilityContextKey, () => report);
app.provide(accountScopeKey, () => scope);
app.mount("#root");
