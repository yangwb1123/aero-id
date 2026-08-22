import * as React from "react";
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AdminAccountFilters, AeroIdClient } from "../api/aeroId";
import {
  AdminAccountDetails,
  adminAccountID,
} from "../components/AdminAccountDetails";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";

const emptyFilters: AdminAccountFilters = {};

export function AdminAccountsPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [draft, setDraft] = React.useState<AdminAccountFilters>(emptyFilters);
  const [filters, setFilters] =
    React.useState<AdminAccountFilters>(emptyFilters);
  const [selectedAccount, setSelectedAccount] = React.useState<string>();
  const load = React.useCallback(
    () => client.listAdminAccounts(filters),
    [client, filters],
  );
  const resource = useAsyncResource(load);

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const updateDraft = (field: keyof AdminAccountFilters, value: string) => {
    setDraft((current) => ({ ...current, [field]: value }));
  };
  const reset = () => {
    setDraft({});
    setFilters({});
  };
  return (
    <section>
      <PageHeader
        title="账户管理"
        description="Snaplink 决定全局账户读取权限；aero-id 仅展示规范账户、来源引用和版本化聚合投影。"
      />
      <IrisCard variant="outline" header="查询条件">
        <form
          className="admin-filter-grid"
          onSubmit={(event) => {
            event.preventDefault();
            setFilters({ ...draft });
          }}
        >
          <IrisFormField label="账户关键词">
            <IrisInput
              type="search"
              value={draft.query ?? ""}
              placeholder="Account ID 或 Canonical UID"
              onChange={(event) => updateDraft("query", event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="状态">
            <IrisInput
              value={draft.status ?? ""}
              placeholder="active / suspended"
              onChange={(event) => updateDraft("status", event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="Home Region">
            <IrisInput
              value={draft.homeRegion ?? ""}
              placeholder="例如 us-west"
              onChange={(event) =>
                updateDraft("homeRegion", event.target.value)
              }
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
      <IrisCard variant="outline" header="规范账户">
        <PagedRecordTable
          page={resource.data!}
          loadPage={(cursor) => client.listAdminAccounts(filters, cursor)}
          columns={[
            { key: "account_id", label: "Account ID", aliases: ["id"] },
            { key: "canonical_uid", label: "Canonical UID" },
            { key: "status", label: "状态", kind: "status" },
            { key: "home_region", label: "Home Region" },
          ]}
          empty="没有符合条件的账户"
          renderActions={(account) => {
            const id = adminAccountID(account);
            return id ? (
              <IrisButton
                size="sm"
                variant="ghost"
                onClick={() => setSelectedAccount(id)}
              >
                查看来源
              </IrisButton>
            ) : null;
          }}
        />
      </IrisCard>
      {selectedAccount ? (
        <AdminAccountDetails
          client={client}
          accountID={selectedAccount}
          close={() => setSelectedAccount(undefined)}
        />
      ) : null}
    </section>
  );
}
