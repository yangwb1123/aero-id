import {
  createApp,
  defineComponent,
  h,
  nextTick,
  ref,
  type Component,
  type Ref,
} from "vue";
import { vi } from "vitest";
import type { CommandIntent, CommandPlan } from "../api/commandIntent";
import type { CapabilityReport } from "../types";
import { accountScopeKey } from "../hooks/accountContext";
import { capabilityContextKey } from "../hooks/capabilityContext";

export const testAccountID = "01800000-0000-7000-8000-000000000002";
export const testOtherAccountID = "01800000-0000-7000-8000-000000000099";
export const testIntentID = "01800000-0000-7000-8000-000000000001";
export const testReceiptID = "01800000-0000-7000-8000-000000000003";
export const testScope = JSON.stringify([
  "https://id.example/v1",
  "https://snaplink.example",
  "console",
  "tenant-a",
  testAccountID,
]);
export const testDatasets = [
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
  { name: "snaplink.identity", source: "snaplink", pii: true },
];
export function commandTestReport(): CapabilityReport {
  const names = [
    "account.self.read",
    "account.self.sync",
    "account.self.export",
    "account.self.erase",
    "operation.read",
    "operation.manage",
    "source.sync",
    "account.export",
    "account.erase",
    "audit.replay",
    "audit.read",
    "audit.export",
    "account.admin.read",
  ];
  return {
    schema_version: 1,
    account_id: testAccountID,
    tenant_id: "tenant-a",
    client_id: "console",
    issuer: "https://snaplink.example",
    home_region: "local",
    local_region: "local",
    generated_at: new Date().toISOString(),
    authorization_version: "unsupported",
    partial: false,
    capabilities: names.map((name) => ({
      name,
      registered: true,
      enabled: true,
      authorization: "allowed",
      authorization_scope: name.startsWith("account.self.") ? "self" : "global",
      available: true,
    })),
    datasets: testDatasets.map((item) => ({
      ...item,
      registered: true,
      enabled: true,
      authorization: "allowed",
      resource_authorization: "unknown",
      status: "unknown",
      fresh: false,
      version: 0,
    })),
  };
}
export function commandTestIntent(
  plan: CommandPlan = { kind: "export", datasets: ["aero-vault.usage"] },
  accountID = testAccountID,
): CommandIntent {
  return {
    intent_id: testIntentID,
    schema_version: 1,
    account_id: accountID,
    tenant_id: "tenant-a",
    client_id: "console",
    issuer: "https://snaplink.example",
    version: 1,
    status: "prepared",
    plan,
    preconditions: {
      account_status: "active",
      home_region: "local",
      writer_epoch: 3,
      resolver_version: "v1",
      snapshots: [],
    },
    confirmation_digest: `v1:${"a".repeat(64)}`,
    destructive:
      plan.kind === "erase" ||
      plan.kind === "callback_replay" ||
      Boolean(
        plan.steps?.some((step) =>
          [step.action, step.compensation_action].some((action) =>
            ["erase", "delete", "source.erase"].includes(action ?? ""),
          ),
        ),
      ),
    authorization_version: "unsupported",
    resource_authorization: "unknown",
    created_at: new Date(Date.now() - 1000).toISOString(),
    expires_at: new Date(Date.now() + 300000).toISOString(),
  };
}
export function executedTestIntent(intent: CommandIntent): CommandIntent {
  return {
    ...intent,
    status: "executed",
    version: 2,
    receipt_id:
      intent.plan.kind === "callback_replay" ? intent.intent_id : testReceiptID,
    receipt_kind:
      intent.plan.kind === "callback_replay"
        ? "callback_replay"
        : intent.plan.kind === "operation"
          ? "operation"
          : "sync_job",
  };
}
export function abandonedTestIntent(intent: CommandIntent): CommandIntent {
  return {
    ...intent,
    status: "abandoned",
    version: 2,
    receipt_id: undefined,
    receipt_kind: undefined,
    abandoned_at: new Date().toISOString(),
  };
}
export function commandTestClient() {
  return {
    commands: {
      prepare: vi.fn(
        async (plan: CommandPlan, _key: string, accountID?: string) =>
          commandTestIntent(plan, accountID),
      ),
      get: vi.fn(async (_id: string, _signal?: AbortSignal) =>
        commandTestIntent(),
      ),
      findByKey: vi.fn(async (_key: string, _signal?: AbortSignal) =>
        commandTestIntent(),
      ),
      abandon: vi.fn(async (intent: CommandIntent) =>
        abandonedTestIntent(intent),
      ),
      execute: vi.fn(async (intent: CommandIntent) =>
        executedTestIntent(intent),
      ),
    },
    listSyncJobs: vi.fn(async () => ({ items: [] })),
    listDatasets: vi.fn(async () => testDatasets),
    getSyncJob: vi.fn(async () => ({
      job_id: testReceiptID,
      account_id: testAccountID,
      tenant_id: "tenant-a",
      actor_uid: "subject",
      job_type: "export",
      status: "pending",
      request: { datasets: ["aero-vault.usage"] },
    })),
    createOperation: vi.fn(),
    createAdminAccountJob: vi.fn(),
    eraseMyAccount: vi.fn(),
    createSyncJob: vi.fn(),
    createExportJob: vi.fn(),
    runOperation: vi.fn(),
  };
}
const apps: ReturnType<typeof createApp>[] = [];
export function mountCommandTest(
  component: Component,
  props: Record<string, unknown>,
  options: {
    report?: Ref<CapabilityReport | undefined>;
    scope?: Ref<string | undefined>;
    provide?: boolean;
  } = {},
) {
  const root = document.createElement("div");
  const report =
    options.report ?? ref<CapabilityReport | undefined>(commandTestReport());
  const scope = options.scope ?? ref<string | undefined>(testScope);
  const app = createApp(component, props);
  if (options.provide !== false) {
    app.provide(capabilityContextKey, () => report.value);
    app.provide(accountScopeKey, () => scope.value);
  }
  apps.push(app);
  app.mount(root);
  return { app, root, report, scope };
}
export function unmountCommandTests(): void {
  apps.splice(0).forEach((app) => app.unmount());
}
export async function flushCommandUI(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    await nextTick();
  }
  await new Promise((resolve) => setTimeout(resolve, 0));
  await nextTick();
}
export function commandButton(
  root: HTMLElement,
  text: string,
): HTMLButtonElement {
  const button = [...root.querySelectorAll("button")].find((item) =>
    item.textContent?.includes(text),
  );
  if (!button) throw new Error(`missing button: ${text}`);
  return button;
}
export function chooseCommandDataset(root: HTMLElement, index: number): void {
  const input = root.querySelectorAll<HTMLInputElement>(
    'input[type="checkbox"]',
  )[index];
  input.checked = true;
  input.dispatchEvent(new Event("change"));
}
export function irisCommandTestStubs() {
  const wrapper = defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        h("div", [slots.header?.(), slots.default?.(), slots.footer?.()]),
  });
  const input = defineComponent({
    props: { modelValue: String, disabled: Boolean, readonly: Boolean },
    emits: ["update:modelValue"],
    setup:
      (props, { emit }) =>
      () =>
        h("input", {
          value: props.modelValue,
          disabled: props.disabled,
          readOnly: props.readonly,
          onInput: (event: Event) =>
            emit("update:modelValue", (event.target as HTMLInputElement).value),
        }),
  });
  return {
    IrisAlert: wrapper,
    IrisCard: wrapper,
    IrisDialogContent: wrapper,
    IrisDialogDescription: wrapper,
    IrisDialogTitle: wrapper,
    IrisFormField: wrapper,
    IrisEmptyState: wrapper,
    IrisTimeline: wrapper,
    IrisSpinner: wrapper,
    IrisBadge: wrapper,
    IrisDialog: defineComponent({
      props: { open: Boolean },
      setup:
        (props, { slots }) =>
        () =>
          props.open ? h("div", slots.default?.()) : null,
    }),
    IrisButton: defineComponent({
      props: { disabled: Boolean, type: String },
      emits: ["click"],
      setup:
        (props, { slots, emit }) =>
        () =>
          h(
            "button",
            {
              type: props.type,
              disabled: props.disabled,
              onClick: () => emit("click"),
            },
            slots.default?.(),
          ),
    }),
    IrisInput: input,
    IrisTextarea: defineComponent({
      props: { modelValue: String },
      emits: ["update:modelValue"],
      setup:
        (props, { emit }) =>
        () =>
          h("textarea", {
            value: props.modelValue,
            onInput: (event: Event) =>
              emit(
                "update:modelValue",
                (event.target as HTMLTextAreaElement).value,
              ),
          }),
    }),
    IrisSelect: defineComponent({
      props: ["modelValue", "items"],
      emits: ["update:modelValue"],
      setup:
        (props, { emit }) =>
        () =>
          h(
            "select",
            {
              value: props.modelValue,
              onChange: (event: Event) =>
                emit(
                  "update:modelValue",
                  (event.target as HTMLSelectElement).value,
                ),
            },
            props.items.map((item: { value: string; label: string }) =>
              h("option", { value: item.value }, item.label),
            ),
          ),
    }),
    IrisCheckbox: defineComponent({
      props: { modelValue: Boolean, disabled: Boolean },
      emits: ["update:modelValue"],
      setup:
        (props, { slots, emit }) =>
        () =>
          h("label", [
            h("input", {
              type: "checkbox",
              checked: props.modelValue,
              disabled: props.disabled,
              onChange: (event: Event) =>
                emit(
                  "update:modelValue",
                  (event.target as HTMLInputElement).checked,
                ),
            }),
            slots.default?.(),
          ]),
    }),
  };
}
