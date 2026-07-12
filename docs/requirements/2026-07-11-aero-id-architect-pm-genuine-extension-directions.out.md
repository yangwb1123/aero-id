Excellent analysis. I've validated each claim against the live codebase. Here's my assessment:

---

## Codebase Validation Results

### Direction 1: Conversation/Thread Model — ⚠️ Partially Correct

There's **more** here than the analysis suggests, which actually strengthens the case:

- Routes exist: `GET /conversations/:uid` and `POST /conversations/:uid/read` in `internal/handler/http/message.go:34-35`
- Service method `Conversation()` and `MarkConversationRead()` exist in `internal/service/message.go`
- Repository `ListConversation()` queries messages between two parties in `internal/repository/message.go:135`

**But**: these are query-time constructs only — `WHERE (from_uid=A AND to_uid=B) OR (from_uid=B AND to_uid=A)`. No `conversations` table, no `conversation_participants` table, no persistent conversation ID on messages. The Domain has no `Conversation` entity. Group chats are impossible without schema changes.

**Verdict**: The gap is real. The existing "conversation" routes are scaffolding that happens to have the right name. The product gap (no persistent threads, no groups) remains.

### Direction 2: File Version History — ✅ Accurate

Confirmed: `domain.File` has no version field. `service/file.go` `Create()` writes a new blob with dedup but no version tracking. No `file_versions` table. `storage_refs` already supports atomic reference counting — the infrastructure for multi-version lifecycle is ready.

### Direction 3: Multi-device Push — ✅ Accurate

Only SSE hub exists. The notification dispatcher's `Pusher` interface (`service.NotificationPusher` in `internal/service/notification.go:74-76`) and the `outbox.NewChain` pattern (`internal/app/app.go:201`) are indeed ready to accept a push handler.

### Direction 4: Distributed Rate Limiting — ✅ Accurate

`internal/pkg/ratelimit/ratelimit.go:8-9` explicitly says: *"Distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope here."* The `Allow(key string) bool` interface is clean enough to extract a backend abstraction.

### Direction 5: Data Lifecycle — ✅ Accurate

Only `internal/storage/gc/sweeper.go` exists. It handles **file blobs only** with `ListSoftDeletedBefore` + `HardDelete`. No sweeper exists for messages, comments, notifications, audit_logs, or outbox dead letters. The sweeper's pattern (config retention + batch size + periodic interval) is clean and generalizable.

---

## One Correction

The analysis references `internal/notification/dispatcher.go` as the current dispatcher. The **live** code uses `internal/service/notification.go`'s `NotificationDispatcher`, wired in `internal/app/app.go:163-178`. The `internal/notification/` package's dispatcher is a parallel/older implementation that's no longer in the wiring path. This doesn't affect the analysis — the interface and chain pattern are the same — but any implementation should target `service.NotificationDispatcher` and the `NotificationPusher` interface.

---

## Recommendation

All five directions are valid. My priority recommendation given the analysis + code evidence:

| Priority | Direction | Why |
|----------|-----------|-----|
| 1️⃣ | **Conversation Model** | The existing pseudo-conversation routes create a UX expectation that can't be met without the persistent model. Most product impact, infrastructure (routes, service methods, repo queries) is already in place — the missing piece is the schema + domain entity. |
| 2️⃣ | **File Version History** | Lowest engineering cost. The `storage_refs` ref-counting infrastructure was built precisely for this. ~40 lines of SQL + ~100 lines of service/repo code. |
| 3️⃣ | **Multi-device Push** | Highest product value for mobile. The outbox chain pattern makes this a clean additive change, but needs a new `internal/push/` package + Firebase/APNs integration. |
| 4️⃣ | **Data Lifecycle** | Important for production hygiene. The sweeper pattern can be directly replicated. |
| 5️⃣ | **Distributed Rate Limiting** | Only matters at multi-replica scale; default-off config suggests the team is aware. |

Would you like me to start implementing any of these? I suggest **Direction 1 (Conversation Model)** as the highest-value next step — I can draft the schema migration, domain entities, repository methods, and service logic. Or **Direction 2 (File Versions)** for the fastest win.
