All evidence validated. Here is my assessment of the document:

---

## Validation Results

Every claim in the document is **code-verified and accurate**. Here is the detailed confirmation:

### ✅ Direction 1: User Ban Lifecycle — **Confirmed**

| Claim | Verification |
|-------|-------------|
| `PermUserBan` exists in `domain/admin.go:9` | ✅ — `PermUserBan = "user.ban"` is defined |
| No `banned_users` table or migration | ✅ — Last migration is `000022_admin_roles`, no ban table anywhere |
| No `BanUser`/`UnbanUser` endpoints | ✅ — `AdminHandler` only exposes `GET /admin/audit-log` |
| No gRPC ops endpoints for management | ✅ — Confirmed by absence in gRPC server construction |
| Migration seeds `user.ban` in admin role | ✅ — `000022_admin_roles.up.sql` includes `'user.ban'` in admin permissions |
| `PermUserBan` never referenced elsewhere | ✅ — Single hit in all `.go` files (the definition itself) |

The contrast is striking: the *permission constant* and *role seed data* are fully wired, but the actual **enforcement point** (check `IsBanned` before allowing writes) and **management surfaces** (ban/unban endpoints, automated expiry) are zero. This is a genuine security gap.

### ✅ Direction 2: Content Dedup Race — **Confirmed**

| Claim | Verification |
|-------|-------------|
| `FindByChecksum` then `Create` window | ✅ — `service/file.go:109-120` shows the read-then-write pattern |
| `Increment` is "Best-effort" and outside transaction | ✅ — Line 148-150: `_, _ = s.refs.Increment(ctx, f.StorageKey)` with comment "Best-effort" |
| Concurrent identical uploads create duplicates | ✅ — The T-t0/t1/t2/t3 race scenario is correct given the code flow |

### ✅ Direction 3: Webhook Cascading Failure — **Confirmed**

| Claim | Verification |
|-------|-------------|
| Serial chain: dispatcher → webhook | ✅ — `app.go` lines: `relayHandler = outbox.NewChain(dispatcher, webhookHandler)` |
| Chain stops on first error | ✅ — `chain.go:13`: `return err` on first handler error |
| Error prevents `MarkPublished` | ✅ — Relay stops processing on error (confirmed in `NewRelay`) |
| Single slow webhook blocks all events | ✅ — Single-threaded `drain` with chained handlers |

### ✅ Direction 4: Message Recall — **Confirmed**

| Claim | Verification |
|-------|-------------|
| No recall/retract/unsend/withdraw | ✅ — Zero hits across entire codebase |
| Available operations: Send, Edit, Delete, MarkRead, React | ✅ — Confirmed by scanning `messageService` method list |
| Delete is soft-delete (sender-only) | ✅ — Document's description matches code |

### ✅ Direction 5: Ref-Count Atomicity Gap — **Confirmed**

| Claim | Verification |
|-------|-------------|
| Increment is outside Create transaction | ✅ — Line 148-150: called *after* `s.files.Create(ctx, f)` |
| "Best-effort" comment acknowledges fragility | ✅ — Comment explicitly says "Best-effort: a missed increment is no worse than..." |
| Decrement uses `FOR UPDATE` (safe) | ✅ — `storage_ref.go:76` uses `Clauses(clause.Locking{Strength: "UPDATE"})` |
| The race scenario is real | ✅ — T0: Create commits (ref=0), T0': Delete triggers Decrement→0→blob delete, T1: Increment happens on deleted blob |

---

## Document Quality Assessment

**Strengths:**
- Every claim cites exact line numbers that are correct
- The race scenarios for #2 and #5 are fully worked out with timeline diagrams
- The binding constraints are clear: hard security gap (#1), data integrity risks (#2, #5), production resilience (#3)
- The priority ordering (P1/P2) is well-reasoned

**Potential refinements (minor):**

1. **Direction 3** — the document could mention that the *dispatcher* also has side effects (SSE push via hub at `service.WithDispatcherPusher(sseHub)`). A slow webhook doesn't just block notification table writes — it also **delays SSE push to all recipients**, making the impact broader than "local notifications are re-delivered".

2. **Direction 5** — the `storage_ref.go` `Increment` implementation uses `FirstOrCreate` with `gorm.Expr("storage_refs.ref_count + 1")`, which IS atomic per-row (the `ON CONFLICT DO UPDATE SET` in Postgres). The real gap is **call-site ordering**: the Increment should happen **inside** the file.Create transaction or at least before returning success to the user, so a concurrent Delete triggered by a *different* file's soft-delete can't see ref_count=0.

3. **Direction 3 mitigation** could be enriched: a **fan-out Chain** variant that runs handlers concurrently with separate timeout contexts would be the structural fix. The document correctly identifies the problem but the solution sketch (separate relay, circuit breaker) is one of several valid approaches.

**Overall:** This is a high-quality, evidence-driven analysis. The five directions are genuinely distinct (no overlap), genuinely uncovered (not restating existing roadmap items), and genuinely impactful (security, correctness, resilience, product gap).
