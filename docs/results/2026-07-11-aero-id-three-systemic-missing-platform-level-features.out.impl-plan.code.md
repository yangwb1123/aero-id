I understand your role as a Code Implementer. You've provided the full role definition. However, I need to know what specific task you'd like me to implement.

Looking at the project context, I can see there are extensive architecture analysis and implementation plan documents. The most recent comprehensive plan is `architecture-scan-2026-07-11.out.impl-plan.md`, which defines tasks across several directions:

**Available implementation tasks** (from the plan):

| Priority | Direction | Key Tasks |
|----------|-----------|-----------|
| **P0** | TASK-000: `actorKey` dedup cleanup | Zero functional change, quick win |
| **P0** | Direction 二: Dead-letter ops (TASK-010~014) | Query interface, metrics, admin endpoints, gRPC, alerting |
| **P1** | Direction 三: Push notifications (TASK-020~024) | Device tokens, FCM/APNs adapter, dispatcher integration |
| **P1** | Direction 一: Group messaging (TASK-030~034) | Conversations, members, messages, notification |
| **P2** | Direction 五: Activity feed (TASK-040~043) | Follow model, timeline, feed query |
| **P2** | Direction 四: File version pipeline (TASK-050~051) | Versions table, version management |

**Please tell me which specific task(s) you want me to implement.** You can choose:
1. A specific task number (e.g., TASK-000, TASK-010, TASK-020)
2. A complete direction (e.g., "Direction 二 - 死信运维")
3. "All P0 tasks first" to start with the highest priority items
4. Any other combination

Once you specify, I'll:
1. Read the relevant `.arch.md` and `.impl-plan.md` documents in detail
2. Understand the current codebase state
3. Implement the code following all constraints (file size limits, function length, complexity, etc.)
4. Verify with `make test && make lint`
