已保存至 `docs/requirements/2026-07-11-aero-id-architecture-scan-five-high-value-expansion-directions.md`（13KB）。

该文档扫描了全量代码库（23 个 `internal/` 包、23 份 migration SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`），在 80+ 份既有分析零覆盖的前提下，识别出五个高价值扩展方向：

| 方向 | 类别 | 优先级 |
|---|---|---|
| **1. 跨实体引用 GC** | 数据完整性 | P1 |
| **2. 每个资源 Webhook** | 集成/自动化 | P2 |
| **3. 乐观锁版本戳** | 数据一致性 | P1 |
| **4. 会话元数据与生命周期** | 产品能力 | P2 |
| **5. 数据可移植性导出** | 合规/信任 | P2–P3 |

每个方向附精确的代码级证据（Go 源码引用 + SQL 模式 + wire 代码）、架构师/产品经理双视角论证，以及分步实施思路。如需要，我可以将这些方向进一步转化为正式的 Implementation Planning RFC，或为某个方向编写更详细的 `docs/` 技术设计文档。
