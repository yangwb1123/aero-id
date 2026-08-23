import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { PageError, PageLoading } from "./PageState";
import {
  RegionPromotionDialog,
  type PendingRegionPromotion,
} from "./RegionPromotionDialog";
import { RecordTable } from "./RecordTable";

export function regionEpoch(directory: JsonRecord): number {
  const value = directory.fenced_epoch;
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
    throw new Error("区域目录缺少有效的 Fenced Epoch");
  return value;
}

function verifyDirectory(directory: JsonRecord, accountID: string): JsonRecord {
  if (directory.account_id !== accountID)
    throw new Error("区域目录响应的 Account ID 不匹配");
  regionEpoch(directory);
  if (typeof directory.home_region !== "string" || !directory.home_region)
    throw new Error("区域目录缺少 Home Region");
  return directory;
}

function verifyPromotion(
  result: JsonRecord,
  accountID: string,
  pending: PendingRegionPromotion,
): void {
  if (
    result.account_id !== accountID ||
    result.target_region !== pending.targetRegion ||
    regionEpoch(result) <= pending.expectedEpoch
  )
    throw new Error("区域提升响应与请求的 CAS 结果不一致");
}

export function RegionDirectoryPanel({
  client,
  accountID,
  promoted,
}: {
  client: AeroIdClient;
  accountID: string;
  promoted(result: JsonRecord): void;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const result = await client.regions.getDirectory(accountID);
    return verifyDirectory(result, accountID);
  }, [accountID, client]);
  const resource = useAsyncResource(load);
  const [targetRegion, setTargetRegion] = React.useState("");
  const [pending, setPending] = React.useState<PendingRegionPromotion>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const prepare = () => {
    setError(undefined);
    const target = targetRegion.trim();
    if (!target) return setError(new Error("目标 Region 不能为空"));
    if (target === resource.data!.home_region)
      return setError(new Error("目标 Region 必须不同于当前 Home Region"));
    setConfirmation("");
    setPending({
      targetRegion: target,
      expectedEpoch: regionEpoch(resource.data!),
      idempotencyKey: crypto.randomUUID(),
    });
  };
  const promote = async () => {
    if (!pending) return;
    setRunning(true);
    setError(undefined);
    try {
      const result = await client.regions.promote({ accountID, ...pending });
      verifyPromotion(result, accountID, pending);
      setPending(undefined);
      setConfirmation("");
      setTargetRegion("");
      promoted(result);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause : new Error("Home Region 提升失败"),
      );
    } finally {
      setRunning(false);
    }
  };
  return (
    <>
      <IrisCard variant="outline" header="Durable Region Directory">
        <RecordTable
          records={[resource.data!]}
          columns={[
            { key: "home_region", label: "当前 Writer Region" },
            { key: "fenced_epoch", label: "Fenced Epoch" },
            { key: "resolver_version", label: "Resolver 版本" },
            { key: "promoted_at", label: "最近提升时间", kind: "date" },
          ]}
        />
        <IrisAlert tone="warning" title="提升是故障处置命令">
          服务端只接受配置允许的 Region，并以当前 Epoch 做一次 CAS；出现
          region.epoch_conflict 时应重新读取目录和现场状态，不要盲目重试旧请求。
        </IrisAlert>
        {error && !pending ? (
          <IrisAlert tone="danger">{error.message}</IrisAlert>
        ) : null}
        <form
          className="region-promotion-row"
          onSubmit={(event) => {
            event.preventDefault();
            prepare();
          }}
        >
          <IrisFormField
            label="目标 Region"
            hint="请输入服务端 account.regions 中已配置的精确标识"
          >
            <IrisInput
              required
              maxLength={64}
              value={targetRegion}
              placeholder="例如 us-east"
              onChange={(event) => setTargetRegion(event.target.value)}
            />
          </IrisFormField>
          <IrisButton type="submit" variant="danger">
            检查提升
          </IrisButton>
        </form>
      </IrisCard>
      <RegionPromotionDialog
        accountID={accountID}
        pending={pending}
        confirmation={confirmation}
        running={running}
        error={error}
        setConfirmation={setConfirmation}
        cancel={() => setPending(undefined)}
        confirm={() => void promote()}
      />
    </>
  );
}
