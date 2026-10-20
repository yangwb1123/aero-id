import { createApp } from "vue";
import { AeroIdClient } from "../src/api/aeroId";
import { capabilityContextKey } from "../src/hooks/capabilityContext";
import { accountScopeKey } from "../src/hooks/accountContext";
import OperationResultsHarness from "./OperationResultsHarness.vue";
import "../src/style.css";
// Ephemeral non-JWT marker, intercepted transport only; never recorded.
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
const app = createApp(OperationResultsHarness, { client });
app.provide(capabilityContextKey, () => report);
app.provide(accountScopeKey, () => scope);
app.mount("#root");
