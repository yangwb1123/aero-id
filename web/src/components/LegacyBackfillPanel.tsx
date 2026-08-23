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
import {
  MigrationCommandDialog,
  type MigrationCommand,
} from "./MigrationCommandDialog";
import { RecordTable } from "./RecordTable";

export function LegacyBackfillPanel({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [limit, setLimit] = React.useState(500);
  const [command, setCommand] = React.useState<MigrationCommand>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();
  const [result, setResult] = React.useState<JsonRecord>();

  const prepare = () => {
    setError(undefined);
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) {
      setError(new Error("Backfill batch 必须是 1–1000 的整数"));
      return;
    }
    setConfirmation("");
    setResult(undefined);
    setCommand({
      kind: "backfill",
      limit,
      idempotencyKey: crypto.randomUUID(),
    });
  };
  const execute = async () => {
    if (command?.kind !== "backfill") return;
    setRunning(true);
    setError(undefined);
    try {
      setResult(
        await client.migration.backfillLegacyIdentities(
          command.limit,
          command.idempotencyKey,
        ),
      );
      setCommand(undefined);
      setConfirmation("");
    } catch (cause) {
      setError(cause instanceof Error ? cause : new Error("Backfill 执行失败"));
    } finally {
      setRunning(false);
    }
  };

  return (
    <>
      <IrisCard variant="outline" header="Legacy Identity Shadow Backfill">
        <IrisAlert tone="info" title="只建立控制面账户引用">
          Backfill 将 legacy UID 映射为 sentinel tenant 下的 shadow account
          identity；不会迁移文件、消息或评论内容，也不会替代 Snaplink
          人类身份权威。
        </IrisAlert>
        <div className="migration-command-row">
          <IrisFormField
            label="单批 UID 上限"
            hint="默认 500，服务端硬上限 1000；未完成时以新的管理意图执行下一批"
          >
            <IrisInput
              type="number"
              min={1}
              max={1000}
              value={limit}
              onChange={(event) => setLimit(Number(event.target.value))}
            />
          </IrisFormField>
          <IrisButton onClick={prepare}>检查并确认 Backfill</IrisButton>
        </div>
        {error && !command ? (
          <IrisAlert tone="danger">{error.message}</IrisAlert>
        ) : null}
        {result ? (
          <>
            <RecordTable
              records={[result]}
              columns={[
                { key: "scanned", label: "已扫描" },
                { key: "created", label: "已创建" },
                { key: "existing", label: "已存在" },
                { key: "skipped", label: "已跳过" },
                { key: "total", label: "估算总数" },
                { key: "tenant", label: "Shadow Tenant" },
                { key: "issuer", label: "Issuer" },
                { key: "completed", label: "本轮是否完成" },
              ]}
            />
            {result.completed !== true ? (
              <IrisAlert tone="warning">
                仍有 UID 待处理。核对 skipped
                与审计事实后，再以新的确认和幂等键执行下一批。
              </IrisAlert>
            ) : null}
          </>
        ) : null}
      </IrisCard>
      <MigrationCommandDialog
        command={command}
        confirmation={confirmation}
        running={running}
        error={error}
        setConfirmation={setConfirmation}
        cancel={() => setCommand(undefined)}
        confirm={() => void execute()}
      />
    </>
  );
}
