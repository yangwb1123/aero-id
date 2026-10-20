import { createApp } from "vue";
import { AeroIdClient } from "../src/api/aeroId";
import { capabilityContextKey } from "../src/hooks/capabilityContext";
import ResourcesHarness from "./ResourcesHarness.vue";
import "../src/style.css";

// Ephemeral non-JWT marker for the intercepted test transport only. No login,
// verified claims or production authorization is synthesized by this harness.
const marker = crypto.randomUUID();
const client = new AeroIdClient(
  new URL("/v1", location.origin).href,
  () => marker,
);
const report = await client.getCapabilities();
const app = createApp(ResourcesHarness, { client });
app.provide(capabilityContextKey, () => report);
app.mount("#root");
