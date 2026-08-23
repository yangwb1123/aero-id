import type { AeroIdClient } from "../api/aeroId";
import { LegacyBackfillPanel } from "../components/LegacyBackfillPanel";
import { LegacyComparePanel } from "../components/LegacyComparePanel";
import { LegacyCutoverPanel } from "../components/LegacyCutoverPanel";
import { PageHeader } from "../components/PageHeader";

export function MigrationPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  return (
    <section>
      <PageHeader
        title="Legacy 身份迁移与切换"
        description="有界建立 shadow account link、比较 legacy content-free digest 与来源摘要，并按租户状态机执行可审计 Cutover；不会把 legacy 内容复制进 aero-id。"
      />
      <LegacyBackfillPanel client={client} />
      <LegacyComparePanel client={client} />
      <LegacyCutoverPanel client={client} />
    </section>
  );
}
