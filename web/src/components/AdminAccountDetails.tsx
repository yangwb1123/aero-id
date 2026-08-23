import * as React from "react";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";
import { AdminAccountActions } from "./AdminAccountActions";
import { PageError, PageLoading } from "./PageState";
import { RecordTable } from "./RecordTable";
import { RegionDirectoryPanel } from "./RegionDirectoryPanel";

function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

export function AdminAccountDetails({
  client,
  accountID,
  close,
}: {
  client: AeroIdClient;
  accountID: string;
  close(): void;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const [account, overview] = await Promise.all([
      client.getAdminAccount(accountID),
      client.getAdminAccountOverview(accountID),
    ]);
    return { account, overview };
  }, [accountID, client]);
  const resource = useAsyncResource(load);
  const [promotionMessage, setPromotionMessage] = React.useState<string>();
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const { account, overview } = resource.data!;
  const accountRecord = account.account ?? overview.account ?? {};
  return (
    <>
      <IrisCard
        variant="outline"
        header={`账户详情 · ${accountID}`}
        footer={
          <div className="detail-actions">
            <IrisButton size="sm" variant="ghost" onClick={close}>
              关闭
            </IrisButton>
          </div>
        }
      >
        {overview.partial ? (
          <IrisAlert tone="warning" title="聚合结果不完整">
            部分来源不可用或投影过期；下方状态保留各来源的新鲜度，不会伪装为完整结果。
          </IrisAlert>
        ) : null}
        <RecordTable
          records={[accountRecord]}
          columns={[
            { key: "canonical_uid", label: "Canonical UID" },
            { key: "status", label: "状态", kind: "status" },
            { key: "home_region", label: "Home Region" },
          ]}
        />
        <h3 className="detail-heading">来源账户</h3>
        <RecordTable
          records={records(account.sources)}
          columns={[
            { key: "source", label: "来源" },
            { key: "source_account_id", label: "来源账户" },
            { key: "status", label: "状态", kind: "status" },
            { key: "source_region", label: "来源区域" },
            { key: "version", label: "版本" },
            { key: "generated_at", label: "生成时间", kind: "date" },
            { key: "last_synced_at", label: "最后同步", kind: "date" },
          ]}
          empty="该账户没有来源关联"
        />
        <h3 className="detail-heading">聚合投影新鲜度</h3>
        <RecordTable
          records={records(overview.snapshots)}
          columns={[
            { key: "dataset", label: "数据集" },
            { key: "source", label: "来源" },
            { key: "status", label: "状态", kind: "status" },
            { key: "source_region", label: "来源区域" },
            { key: "version", label: "版本" },
            { key: "generated_at", label: "生成时间", kind: "date" },
            { key: "last_synced_at", label: "最后同步", kind: "date" },
          ]}
          empty="该账户尚无聚合投影"
        />
        <h3 className="detail-heading">跨来源成员关系</h3>
        <RecordTable
          records={records(account.memberships)}
          columns={[
            { key: "source", label: "来源" },
            { key: "scope_type", label: "作用域类型" },
            { key: "scope_id", label: "作用域 ID" },
            { key: "role", label: "角色" },
            { key: "status", label: "状态", kind: "status" },
            { key: "last_synced_at", label: "最后同步", kind: "date" },
          ]}
          empty="该账户没有成员关系"
        />
      </IrisCard>
      {promotionMessage ? (
        <IrisAlert tone="success">{promotionMessage}</IrisAlert>
      ) : null}
      <RegionDirectoryPanel
        client={client}
        accountID={accountID}
        promoted={(result) => {
          setPromotionMessage(
            `Writer 已提升到 ${String(result.target_region)}，新 Fenced Epoch 为 ${String(result.fenced_epoch)}。`,
          );
          resource.reload();
        }}
      />
      <AdminAccountActions client={client} accountID={accountID} />
    </>
  );
}

export function adminAccountID(record: JsonRecord): string | undefined {
  const value = record.account_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
