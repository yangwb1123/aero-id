import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisEmptyState,
  IrisTimeline,
  type IrisTimelineItem,
  type IrisTimelineVariant,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord, PageData } from "../types";
import { mergePage } from "./PagedRecordTable";
import { PageError, PageLoading } from "./PageState";
import { RecordTable } from "./RecordTable";

function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

function displayDate(value: unknown): string {
  const date = new Date(String(value ?? ""));
  return Number.isNaN(date.getTime())
    ? String(value ?? "")
    : date.toLocaleString();
}

function auditVariant(action: string): IrisTimelineVariant {
  if (/(fail|denied|reject|error)/i.test(action)) return "danger";
  if (/(unknown|reconcile|compensat)/i.test(action)) return "warning";
  if (/(complet|succeed|deliver)/i.test(action)) return "success";
  return "info";
}

function auditItems(events: JsonRecord[]): IrisTimelineItem[] {
  return events.map((event, index) => {
    const action = String(event.action ?? "operation.event");
    const source = String(event.source ?? "aero-id");
    const resource = [event.resource_type, event.resource_id]
      .filter(Boolean)
      .join(" · ");
    return {
      key: String(event.event_id ?? event.id ?? index),
      title: action,
      time: displayDate(event.occurred_at ?? event.created_at),
      description: resource ? `${source} · ${resource}` : source,
      variant: auditVariant(action),
    };
  });
}

function AuditTimelinePage({
  client,
  operationID,
  initial,
}: {
  client: AeroIdClient;
  operationID: string;
  initial: PageData<JsonRecord>;
}): React.ReactElement {
  const [page, setPage] = React.useState(initial);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState<Error>();

  React.useEffect(() => {
    setPage(initial);
    setLoading(false);
    setError(undefined);
  }, [initial]);

  const loadMore = async () => {
    const cursor = page.nextCursor;
    if (!cursor || loading) return;
    setLoading(true);
    setError(undefined);
    try {
      setPage(
        mergePage(
          page,
          await client.listOperationAudit(operationID, cursor),
          cursor,
        ),
      );
    } catch (reason) {
      setError(
        reason instanceof Error ? reason : new Error("审计时间线读取失败"),
      );
    } finally {
      setLoading(false);
    }
  };

  if (page.items.length === 0)
    return (
      <IrisEmptyState
        title="暂无审计事实"
        description="此操作尚未产生当前调用者可查看的审计事实。"
      />
    );
  return (
    <>
      <IrisTimeline items={auditItems(page.items)} />
      {error ? (
        <IrisAlert tone="danger">
          <div className="alert-actions">
            <span>{error.message}</span>
            <IrisButton
              size="sm"
              variant="outline"
              onClick={() => void loadMore()}
            >
              重试
            </IrisButton>
          </div>
        </IrisAlert>
      ) : null}
      {page.nextCursor && !error ? (
        <div className="pagination-actions">
          <IrisButton
            size="sm"
            variant="outline"
            loading={loading}
            onClick={() => void loadMore()}
          >
            加载更早事实
          </IrisButton>
        </div>
      ) : null}
    </>
  );
}

function OperationAuditTimeline({
  client,
  operationID,
}: {
  client: AeroIdClient;
  operationID: string;
}): React.ReactElement {
  const load = React.useCallback(
    () => client.listOperationAudit(operationID),
    [client, operationID],
  );
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;
  return (
    <IrisCard variant="outline" header="脱敏审计时间线">
      <AuditTimelinePage
        client={client}
        operationID={operationID}
        initial={resource.data!}
      />
    </IrisCard>
  );
}

export function OperationDetails({
  client,
  operationID,
  close,
  changed,
}: {
  client: AeroIdClient;
  operationID: string;
  close(): void;
  changed(): void;
}): React.ReactElement {
  const load = React.useCallback(
    () => client.getOperation(operationID),
    [client, operationID],
  );
  const resource = useAsyncResource(load);
  const [reconciling, setReconciling] = React.useState(false);
  const [actionError, setActionError] = React.useState<Error>();

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const operation = resource.data!;
  const status = String(operation.status ?? "");
  const steps = records(operation.steps);
  const reconcile = async () => {
    setReconciling(true);
    setActionError(undefined);
    try {
      resource.replace(await client.reconcileOperation(operationID));
      changed();
    } catch (reason) {
      setActionError(
        reason instanceof Error ? reason : new Error("操作对账失败"),
      );
    } finally {
      setReconciling(false);
    }
  };

  return (
    <div className="operation-details">
      <IrisCard
        variant="outline"
        header={`Operation 详情 · ${operationID}`}
        footer={
          <div className="detail-actions">
            <IrisButton size="sm" variant="ghost" onClick={close}>
              关闭
            </IrisButton>
            {status === "unknown" ? (
              <IrisButton
                size="sm"
                variant="outline"
                loading={reconciling}
                onClick={() => void reconcile()}
              >
                对账未知结果
              </IrisButton>
            ) : null}
          </div>
        }
      >
        {actionError ? (
          <IrisAlert tone="danger">{actionError.message}</IrisAlert>
        ) : null}
        {status === "unknown" ? (
          <IrisAlert tone="warning" title="远端结果尚未确认">
            对账会查询来源 operation ID，不会盲目重发原始命令。
          </IrisAlert>
        ) : null}
        {operation.error_code ? (
          <IrisAlert tone="danger" title={String(operation.error_code)}>
            {String(operation.error_message ?? "操作执行失败")}
          </IrisAlert>
        ) : null}
        <RecordTable
          records={[operation]}
          columns={[
            { key: "operation_type", label: "类型", aliases: ["type"] },
            { key: "status", label: "状态", kind: "status" },
            { key: "home_region", label: "Home Region" },
            { key: "request_id", label: "Request ID" },
            { key: "created_at", label: "创建时间", kind: "date" },
            { key: "completed_at", label: "完成时间", kind: "date" },
          ]}
        />
        <h3 className="detail-heading">Saga 步骤</h3>
        <RecordTable
          records={steps}
          columns={[
            { key: "step_key", label: "步骤" },
            { key: "source", label: "来源" },
            { key: "action", label: "动作" },
            { key: "status", label: "状态", kind: "status" },
            { key: "attempt", label: "尝试" },
            { key: "source_operation_id", label: "来源 Operation ID" },
            {
              key: "compensation_status",
              label: "补偿状态",
              kind: "status",
            },
          ]}
          empty="暂无 Saga 步骤"
        />
        <div className="detail-json-grid">
          <div>
            <h3>请求</h3>
            <pre className="json-preview">
              {jsonPreview(operation.request_payload)}
            </pre>
          </div>
          <div>
            <h3>结果</h3>
            <pre className="json-preview">{jsonPreview(operation.result)}</pre>
          </div>
        </div>
      </IrisCard>
      <OperationAuditTimeline client={client} operationID={operationID} />
    </div>
  );
}

export function operationID(record: JsonRecord): string | undefined {
  const value = record.operation_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
