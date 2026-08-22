import * as React from "react";
import { IrisAlert, IrisButton, IrisCard, IrisInput } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { Profile } from "../types";

const fields = [
  ["display_name", "显示名称"],
  ["avatar_url", "头像 URL"],
  ["locale", "语言"],
  ["timezone", "时区"],
] as const;

export function ProfilePage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(() => client.getProfile(), [client]);
  const resource = useAsyncResource(load);
  const [draft, setDraft] = React.useState<Partial<Profile>>({});
  const [saving, setSaving] = React.useState(false);
  const [saved, setSaved] = React.useState(false);
  const [saveError, setSaveError] = React.useState<Error>();

  React.useEffect(() => {
    if (resource.data) setDraft(resource.data);
  }, [resource.data]);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const save = async () => {
    setSaving(true);
    setSaved(false);
    setSaveError(undefined);
    try {
      const updated = await client.updateProfile({
        display_name: draft.display_name ?? "",
        avatar_url: draft.avatar_url ?? "",
        locale: draft.locale ?? "",
        timezone: draft.timezone ?? "",
      });
      resource.replace(updated);
      setSaved(true);
    } catch (reason) {
      setSaveError(reason instanceof Error ? reason : new Error("保存失败"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section>
      <PageHeader
        title="个人资料"
        description="这里只修改 aero-id 明确拥有的展示和偏好字段。"
      />
      <IrisAlert tone="info" title="身份字段由 Snaplink 管理">
        邮箱、电话、验证状态、MFA 和账户生命周期不能在这里修改。
      </IrisAlert>
      {saveError ? (
        <IrisAlert tone="danger">{saveError.message}</IrisAlert>
      ) : null}
      {saved ? <IrisAlert tone="success">资料已保存。</IrisAlert> : null}
      <IrisCard variant="outline">
        <form
          className="profile-form"
          onSubmit={(event) => {
            event.preventDefault();
            void save();
          }}
        >
          {fields.map(([key, label]) => (
            <label className="form-row" key={key}>
              <span>{label}</span>
              <IrisInput
                value={String(draft[key] ?? "")}
                onChange={(event) =>
                  setDraft((value) => ({ ...value, [key]: event.target.value }))
                }
              />
            </label>
          ))}
          <div className="form-actions">
            <IrisButton type="submit" loading={saving}>
              保存资料
            </IrisButton>
          </div>
        </form>
      </IrisCard>
    </section>
  );
}
