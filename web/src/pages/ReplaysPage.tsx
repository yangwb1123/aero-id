import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { ReplayCreateInput, ReplayMode } from "../api/replay";
import { PageHeader } from "../components/PageHeader";
import { ProjectionReplayPanel } from "../components/ProjectionReplayPanel";
import {
  ReplayCommandEditor,
  newReplayCommand,
  parseReplayCommands,
  type ReplayCommandDraft,
} from "../components/ReplayCommandEditor";
import { ReplayConfirmationDialog } from "../components/ReplayConfirmationDialog";
import { ReplayResult } from "../components/ReplayResult";
import type { JsonRecord } from "../types";

const modeItems = [
  { value: "forensic", label: "Forensic（仅重建事实）" },
  { value: "dry-run", label: "Dry-run（模拟、不执行）" },
  { value: "controlled", label: "Controlled（真实命令）" },
];

function actionLabel(mode: ReplayMode): string {
  if (mode === "controlled") return "检查并确认 Controlled Replay";
  if (mode === "dry-run") return "运行 Dry-run";
  return "执行 Forensic 重建";
}

export function replayContinuation(
  input: ReplayCreateInput | undefined,
  cursor: string,
): ReplayCreateInput {
  if (!input || input.mode === "controlled")
    throw new Error("只有 Forensic 或 Dry-run 结果可以继续读取下一窗口");
  return { ...input, cursor };
}

function replayTimestamp(value: string, label: string): string | undefined {
  const normalized = value.trim();
  if (!normalized) return undefined;
  const timestamp = new Date(normalized);
  if (Number.isNaN(timestamp.getTime())) throw new Error(`${label} 时间无效`);
  return timestamp.toISOString();
}

export function replayTimeWindow(
  fromValue: string,
  toValue: string,
): {
  from?: string;
  to?: string;
} {
  const from = replayTimestamp(fromValue, "开始");
  const to = replayTimestamp(toValue, "结束");
  if (from && to && Date.parse(from) >= Date.parse(to))
    throw new Error("Replay 开始时间必须早于结束时间");
  return { from, to };
}

function validateReplayScope(accountID: string, operationID: string): void {
  if (!accountID && !operationID)
    throw new Error("Forensic 和 Dry-run 至少需要 Account ID 或 Operation ID");
}

function validateControlledReplay(
  mode: ReplayMode,
  accountID: string,
  idempotencyKey: string,
  commandCount: number,
): void {
  if (mode !== "controlled") return;
  if (!accountID) throw new Error("Controlled Replay 必须指定 Account ID");
  if (commandCount === 0) throw new Error("Controlled Replay 至少需要一个命令");
  if (!idempotencyKey) throw new Error("Controlled Replay 必须使用新的幂等键");
}

export function ReplaysPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [mode, setMode] = React.useState<ReplayMode>("forensic");
  const [accountID, setAccountID] = React.useState("");
  const [operationID, setOperationID] = React.useState("");
  const [fromTime, setFromTime] = React.useState("");
  const [toTime, setToTime] = React.useState("");
  const [limit, setLimit] = React.useState(100);
  const [idempotencyKey, setIdempotencyKey] = React.useState("");
  const [commands, setCommands] = React.useState<ReplayCommandDraft[]>([]);
  const [pending, setPending] = React.useState<ReplayCreateInput>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();
  const [result, setResult] = React.useState<JsonRecord>();
  const [lastInput, setLastInput] = React.useState<ReplayCreateInput>();

  const changeMode = (value: unknown) => {
    const next = String(value) as ReplayMode;
    setMode(next);
    setError(undefined);
    if (next === "controlled") {
      if (!idempotencyKey) setIdempotencyKey(crypto.randomUUID());
      if (commands.length === 0) setCommands([newReplayCommand()]);
    } else if (next === "forensic") {
      setCommands([]);
    }
  };
  const buildInput = (): ReplayCreateInput => {
    const normalizedAccountID = accountID.trim();
    const normalizedOperationID = operationID.trim();
    const normalizedKey = idempotencyKey.trim();
    validateReplayScope(normalizedAccountID, normalizedOperationID);
    if (limit < 1 || limit > 100)
      throw new Error("单次重建事实数量必须在 1–100 之间");
    const parsedCommands = parseReplayCommands(commands);
    validateControlledReplay(
      mode,
      normalizedAccountID,
      normalizedKey,
      parsedCommands.length,
    );
    const window =
      mode === "controlled" ? {} : replayTimeWindow(fromTime, toTime);
    return {
      mode,
      accountID: normalizedAccountID || undefined,
      operationID: normalizedOperationID || undefined,
      ...window,
      limit,
      idempotencyKey: mode === "controlled" ? normalizedKey : undefined,
      confirmed: false,
      commands: mode === "forensic" ? [] : parsedCommands,
    };
  };
  const execute = async (input: ReplayCreateInput) => {
    setRunning(true);
    setError(undefined);
    try {
      const nextResult = await client.replays.create(input);
      setResult(nextResult);
      setLastInput(input);
      setPending(undefined);
      setConfirmation("");
    } catch (reason) {
      setError(reason instanceof Error ? reason : new Error("Replay 执行失败"));
    } finally {
      setRunning(false);
    }
  };
  const prepare = () => {
    setError(undefined);
    try {
      const input = buildInput();
      if (input.mode === "controlled") {
        setConfirmation("");
        setPending(input);
      } else {
        void execute(input);
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason : new Error("Replay 输入无效"));
    }
  };
  const continueWindow = (cursor: string) => {
    try {
      void execute(replayContinuation(lastInput, cursor));
    } catch (reason) {
      setError(reason instanceof Error ? reason : new Error("Replay 游标无效"));
    }
  };
  return (
    <section>
      <PageHeader
        title="审计 Replay"
        description="Forensic 只重建事实，Dry-run 只模拟，Controlled 创建全新的持久化 Operation；三种模式绝不复用历史凭据。"
      />
      <IrisCard variant="outline" header="Replay 作用域与模式">
        {error && !pending ? (
          <IrisAlert tone="danger">{error.message}</IrisAlert>
        ) : null}
        <form
          onSubmit={(event) => {
            event.preventDefault();
            prepare();
          }}
        >
          <div className="replay-scope-grid">
            <IrisFormField label="模式">
              <IrisSelect
                items={modeItems}
                value={mode}
                onValueChange={changeMode}
              />
            </IrisFormField>
            <IrisFormField label="Account ID" required={mode === "controlled"}>
              <IrisInput
                value={accountID}
                onChange={(event) => setAccountID(event.target.value)}
              />
            </IrisFormField>
            <IrisFormField
              label="Operation ID"
              hint={
                mode === "controlled"
                  ? "可选历史来源；服务端要求它属于同一 Account"
                  : "可单独作为审计事实重建作用域"
              }
            >
              <IrisInput
                value={operationID}
                onChange={(event) => setOperationID(event.target.value)}
              />
            </IrisFormField>
            <IrisFormField label="事实上限" hint="服务端单批最大 100">
              <IrisInput
                type="number"
                min={1}
                max={100}
                value={limit}
                onChange={(event) => setLimit(Number(event.target.value))}
              />
            </IrisFormField>
            {mode !== "controlled" ? (
              <>
                <IrisFormField
                  label="开始时间"
                  hint="包含；输入本地时间或 RFC3339"
                >
                  <IrisInput
                    placeholder="2026-08-20T10:00"
                    value={fromTime}
                    onChange={(event) => setFromTime(event.target.value)}
                  />
                </IrisFormField>
                <IrisFormField label="结束时间" hint="不包含；必须晚于开始时间">
                  <IrisInput
                    placeholder="2026-08-21T10:00"
                    value={toTime}
                    onChange={(event) => setToTime(event.target.value)}
                  />
                </IrisFormField>
              </>
            ) : null}
          </div>
          {mode === "controlled" ? (
            <div className="replay-key-row">
              <IrisFormField
                label="本次新幂等键"
                hint="请求超时后使用同一个键安全重试；新意图必须生成新键"
              >
                <IrisInput value={idempotencyKey} readOnly />
              </IrisFormField>
              <IrisButton
                type="button"
                variant="outline"
                onClick={() => setIdempotencyKey(crypto.randomUUID())}
              >
                生成新键
              </IrisButton>
            </div>
          ) : null}
          {mode !== "forensic" ? (
            <>
              <h3 className="detail-heading">
                {mode === "dry-run" ? "模拟命令（可选）" : "真实命令"}
              </h3>
              <ReplayCommandEditor commands={commands} change={setCommands} />
            </>
          ) : null}
          <div className="replay-submit-row">
            <IrisButton type="submit" loading={running}>
              {actionLabel(mode)}
            </IrisButton>
          </div>
        </form>
      </IrisCard>
      {result ? (
        <ReplayResult
          client={client}
          result={result}
          clear={() => {
            setResult(undefined);
            setLastInput(undefined);
          }}
          continueWindow={continueWindow}
          continuing={running}
        />
      ) : null}
      <ProjectionReplayPanel client={client} />
      <ReplayConfirmationDialog
        input={pending}
        confirmation={confirmation}
        setConfirmation={setConfirmation}
        running={running}
        error={error}
        cancel={() => {
          setPending(undefined);
          setError(undefined);
        }}
        confirm={() => {
          if (pending) void execute({ ...pending, confirmed: true });
        }}
      />
    </section>
  );
}
