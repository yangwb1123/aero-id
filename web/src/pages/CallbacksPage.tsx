import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisTextarea,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import {
  CallbackCommandDialog,
  CallbackSecretDialog,
  type CallbackCommand,
  type CallbackSecret,
} from "../components/CallbackDialogs";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";

function callbackID(record: JsonRecord): string | undefined {
  const value = record.id ?? record.subscription_id;
  return typeof value === "string" && value ? value : undefined;
}

function eventTypes(value: string): string[] {
  return [...new Set(value.split(/[\n,]/).map((item) => item.trim()))].filter(
    Boolean,
  );
}

export function CallbacksPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(() => client.callbacks.list(), [client]);
  const resource = useAsyncResource(load);
  const [endpoint, setEndpoint] = React.useState("");
  const [eventTypeText, setEventTypeText] = React.useState("");
  const [creating, setCreating] = React.useState(false);
  const [createError, setCreateError] = React.useState<Error>();
  const [secret, setSecret] = React.useState<CallbackSecret>();
  const [deliveryID, setDeliveryID] = React.useState("");
  const [command, setCommand] = React.useState<CallbackCommand>();
  const [commandRunning, setCommandRunning] = React.useState(false);
  const [commandError, setCommandError] = React.useState<Error>();
  const [message, setMessage] = React.useState<string>();

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const create = async () => {
    const filters = eventTypes(eventTypeText);
    if (filters.length === 0) {
      setCreateError(new Error("至少填写一个允许投递的事件类型"));
      return;
    }
    setCreating(true);
    setCreateError(undefined);
    try {
      const result = await client.callbacks.create({
        endpoint: endpoint.trim(),
        eventTypes: filters,
      });
      const id = callbackID(result.subscription);
      if (!id || !result.secret)
        throw new Error("订阅已响应，但缺少一次性 Secret 或订阅 ID");
      setSecret({ subscriptionID: id, value: result.secret });
      setEndpoint("");
      setEventTypeText("");
      resource.replace({
        items: [result.subscription, ...resource.data!.items],
        nextCursor: resource.data!.nextCursor,
      });
    } catch (reason) {
      setCreateError(
        reason instanceof Error ? reason : new Error("Callback 注册失败"),
      );
    } finally {
      setCreating(false);
    }
  };
  const runCommand = async () => {
    if (!command) return;
    setCommandRunning(true);
    setCommandError(undefined);
    setMessage(undefined);
    try {
      if (command.kind === "delete") {
        await client.callbacks.deleteSubscription(command.id);
        setMessage("Callback 订阅已删除，并已写入审计事实。");
        resource.replace({
          items: resource.data!.items.filter(
            (subscription) => callbackID(subscription) !== command.id,
          ),
          nextCursor: resource.data!.nextCursor,
        });
      } else {
        await client.callbacks.replayDelivery(command.id);
        setMessage(`Delivery ${command.id} 已进入重试队列。`);
        setDeliveryID("");
      }
      setCommand(undefined);
    } catch (reason) {
      setCommandError(
        reason instanceof Error ? reason : new Error("Callback 命令失败"),
      );
    } finally {
      setCommandRunning(false);
    }
  };
  return (
    <section>
      <PageHeader
        title="Callback 治理"
        description="端点注册由服务端执行 HTTPS challenge、DNS/私网策略和受控 egress；Secret 仅在创建成功后显示一次。"
      />
      {message ? <IrisAlert tone="success">{message}</IrisAlert> : null}
      <IrisCard variant="outline" header="注册 Callback 端点">
        {createError ? (
          <IrisAlert tone="danger">{createError.message}</IrisAlert>
        ) : null}
        <form
          className="callback-create-grid"
          onSubmit={(event) => {
            event.preventDefault();
            void create();
          }}
        >
          <IrisFormField
            label="HTTPS Endpoint"
            hint="端点必须回显服务端发送的 challenge"
          >
            <IrisInput
              type="url"
              required
              value={endpoint}
              placeholder="https://callback.example.com/aero/events"
              onChange={(event) => setEndpoint(event.target.value)}
            />
          </IrisFormField>
          <IrisFormField
            label="允许的事件类型"
            hint="使用逗号或换行分隔；最多 64 个"
          >
            <IrisTextarea
              required
              value={eventTypeText}
              placeholder="operation.completed, export.ready"
              onChange={(event) => setEventTypeText(event.target.value)}
            />
          </IrisFormField>
          <div className="admin-filter-actions">
            <IrisButton type="submit" loading={creating}>
              验证并注册
            </IrisButton>
          </div>
        </form>
      </IrisCard>
      <IrisCard variant="outline" header="当前订阅">
        <PagedRecordTable
          page={resource.data!}
          loadPage={(cursor) => client.callbacks.list(cursor)}
          columns={[
            { key: "id", label: "订阅 ID", aliases: ["subscription_id"] },
            { key: "endpoint", label: "Endpoint" },
            { key: "event_types", label: "事件类型" },
            { key: "verified", label: "已验证" },
            { key: "enabled", label: "已启用" },
          ]}
          empty="暂无 Callback 订阅"
          renderActions={(subscription) => {
            const id = callbackID(subscription);
            return id ? (
              <IrisButton
                size="sm"
                variant="danger"
                onClick={() => {
                  setCommandError(undefined);
                  setCommand({
                    kind: "delete",
                    id,
                    endpoint: String(subscription.endpoint ?? ""),
                  });
                }}
              >
                删除
              </IrisButton>
            ) : null;
          }}
        />
      </IrisCard>
      <IrisCard variant="outline" header="人工重放投递">
        <IrisAlert tone="warning" title="只重放明确识别的失败投递">
          当前服务端没有投递列表 API。请从脱敏审计事实或运行手册取得 Delivery
          ID，确认远端幂等后再重放。
        </IrisAlert>
        <div className="callback-replay-row">
          <IrisFormField label="Delivery ID">
            <IrisInput
              value={deliveryID}
              onChange={(event) => setDeliveryID(event.target.value)}
            />
          </IrisFormField>
          <IrisButton
            variant="outline"
            disabled={!deliveryID.trim()}
            onClick={() => {
              setCommandError(undefined);
              setCommand({ kind: "replay", id: deliveryID.trim() });
            }}
          >
            重放投递
          </IrisButton>
        </div>
      </IrisCard>
      <CallbackSecretDialog
        secret={secret}
        discard={() => setSecret(undefined)}
      />
      <CallbackCommandDialog
        command={command}
        running={commandRunning}
        error={commandError}
        cancel={() => setCommand(undefined)}
        confirm={() => void runCommand()}
      />
    </section>
  );
}
