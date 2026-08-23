import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/react";
import type { ReplayCommand } from "../api/replay";

export interface ReplayCommandDraft {
  id: string;
  source: ReplayCommand["source"];
  action: ReplayCommand["action"];
  payload: string;
}

const sourceItems = ["snaplink", "aero-im", "aero-vault"].map((value) => ({
  value,
  label: value,
}));
const actionItems = [
  "sync",
  "summary",
  "source.summary",
  "erase",
  "delete",
  "source.erase",
].map((value) => ({ value, label: value }));

export function newReplayCommand(): ReplayCommandDraft {
  return {
    id: crypto.randomUUID(),
    source: "aero-im",
    action: "summary",
    payload: "{}",
  };
}

export function parseReplayCommands(
  drafts: ReplayCommandDraft[],
): ReplayCommand[] {
  return drafts.map((draft, index) => {
    let payload: unknown;
    try {
      payload = JSON.parse(draft.payload || "{}");
    } catch {
      throw new Error(`第 ${index + 1} 个命令的 Payload 不是有效 JSON`);
    }
    if (!payload || typeof payload !== "object" || Array.isArray(payload))
      throw new Error(`第 ${index + 1} 个命令的 Payload 必须是 JSON 对象`);
    return {
      source: draft.source,
      action: draft.action,
      payload: payload as Record<string, unknown>,
    };
  });
}

export function ReplayCommandEditor({
  commands,
  change,
}: {
  commands: ReplayCommandDraft[];
  change(commands: ReplayCommandDraft[]): void;
}): React.ReactElement {
  const update = (id: string, patch: Partial<ReplayCommandDraft>) => {
    change(
      commands.map((command) =>
        command.id === id ? { ...command, ...patch } : command,
      ),
    );
  };
  return (
    <div className="replay-command-list">
      {commands.map((command, index) => (
        <IrisCard
          key={command.id}
          variant="outline"
          header={`命令 ${index + 1}`}
          footer={
            <div className="detail-actions">
              <IrisButton
                size="sm"
                variant="ghost"
                onClick={() =>
                  change(commands.filter((item) => item.id !== command.id))
                }
              >
                移除
              </IrisButton>
            </div>
          }
        >
          <div className="replay-command-grid">
            <IrisFormField label="来源">
              <IrisSelect
                items={sourceItems}
                value={command.source}
                onValueChange={(value) =>
                  update(command.id, {
                    source: String(value) as ReplayCommand["source"],
                  })
                }
              />
            </IrisFormField>
            <IrisFormField label="Allow-list 动作">
              <IrisSelect
                items={actionItems}
                value={command.action}
                onValueChange={(value) =>
                  update(command.id, {
                    action: String(value) as ReplayCommand["action"],
                  })
                }
              />
            </IrisFormField>
            <IrisFormField label="Payload JSON">
              <IrisTextarea
                value={command.payload}
                autosize
                maxRows={8}
                onChange={(event) =>
                  update(command.id, { payload: event.target.value })
                }
              />
            </IrisFormField>
          </div>
        </IrisCard>
      ))}
      <IrisButton
        variant="outline"
        disabled={commands.length >= 32}
        onClick={() => change([...commands, newReplayCommand()])}
      >
        添加命令（{commands.length}/32）
      </IrisButton>
    </div>
  );
}
