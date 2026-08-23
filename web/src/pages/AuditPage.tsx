import * as React from "react";
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AeroIdClient, AuditEventFilters } from "../api/aeroId";
import {
  AuditEventDetails,
  auditEventID,
} from "../components/AuditEventDetails";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";

const emptyFilters: AuditEventFilters = {};

export function AuditPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [draft, setDraft] = React.useState<AuditEventFilters>(emptyFilters);
  const [filters, setFilters] = React.useState<AuditEventFilters>(emptyFilters);
  const [selectedEvent, setSelectedEvent] = React.useState<JsonRecord>();
  const load = React.useCallback(
    () => client.listAuditEvents(filters),
    [client, filters],
  );
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const updateDraft = (field: keyof AuditEventFilters, value: string) => {
    setDraft((current) => ({ ...current, [field]: value }));
  };
  const reset = () => {
    setDraft({});
    setFilters({});
  };
  return (
    <section>
      <PageHeader
        title="审计台账"
        description="查询追加式、脱敏审计事实；链验证由 aero-id 按来源分区重新计算，不声明不存在的全局总序。"
      />
      <IrisCard variant="outline" header="审计筛选">
        <form
          className="audit-filter-grid"
          onSubmit={(event) => {
            event.preventDefault();
            setFilters({ ...draft });
          }}
        >
          <IrisFormField label="来源">
            <IrisInput
              value={draft.source ?? ""}
              placeholder="aero-id / snaplink / aero-im"
              onChange={(event) => updateDraft("source", event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="动作">
            <IrisInput
              value={draft.action ?? ""}
              placeholder="例如 operation.reconcile"
              onChange={(event) => updateDraft("action", event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="Operation ID">
            <IrisInput
              value={draft.operationID ?? ""}
              onChange={(event) =>
                updateDraft("operationID", event.target.value)
              }
            />
          </IrisFormField>
          <IrisFormField label="Account ID">
            <IrisInput
              value={draft.accountID ?? ""}
              onChange={(event) => updateDraft("accountID", event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="Request ID">
            <IrisInput
              value={draft.requestID ?? ""}
              onChange={(event) => updateDraft("requestID", event.target.value)}
            />
          </IrisFormField>
          <div className="admin-filter-actions">
            <IrisButton type="button" variant="ghost" onClick={reset}>
              重置
            </IrisButton>
            <IrisButton type="submit">查询</IrisButton>
          </div>
        </form>
      </IrisCard>
      <IrisCard variant="outline" header="审计事实">
        <PagedRecordTable
          page={resource.data!}
          loadPage={(cursor) => client.listAuditEvents(filters, cursor)}
          columns={[
            { key: "occurred_at", label: "发生时间", kind: "date" },
            { key: "action", label: "动作" },
            { key: "source", label: "来源" },
            { key: "actor_uid", label: "Actor UID" },
            { key: "resource_type", label: "资源类型" },
            { key: "resource_id", label: "资源 ID" },
            { key: "operation_id", label: "Operation ID" },
          ]}
          empty="没有符合条件的审计事实"
          renderActions={(event) =>
            auditEventID(event) ? (
              <IrisButton
                size="sm"
                variant="ghost"
                onClick={() => setSelectedEvent(event)}
              >
                查看证据
              </IrisButton>
            ) : null
          }
        />
      </IrisCard>
      {selectedEvent ? (
        <AuditEventDetails
          client={client}
          event={selectedEvent}
          close={() => setSelectedEvent(undefined)}
        />
      ) : null}
    </section>
  );
}
