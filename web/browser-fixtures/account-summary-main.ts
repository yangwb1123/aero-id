import { createApp } from "vue";
import { AeroIdClient } from "../src/api/aeroId";
import { capabilityContextKey } from "../src/hooks/capabilityContext";
import { accountScopeKey } from "../src/hooks/accountContext";
import AccountSummaryHarness from "./AccountSummaryHarness.vue";
import "../src/style.css";
// In-memory non-JWT marker for the intercepted fixture transport, never saved/logged.
const marker = crypto.randomUUID();
const base = new URL("/v1", location.origin).href;
const client = new AeroIdClient(base, () => marker);
// Fixed synthetic caller context, independent of the intercepted capability reply.
// This is not a login/claims proof and never enables a production auth path.
const scope = JSON.stringify([
  base,
  "https://fixture.invalid",
  "browser-fixture",
  "tenant-demo",
  "01800000-0000-7000-8000-000000000002",
]);
const report = await client.getCapabilities();
const app = createApp(AccountSummaryHarness, { client });
app.provide(capabilityContextKey, () => report);
app.provide(accountScopeKey, () => scope);
app.mount("#root");
