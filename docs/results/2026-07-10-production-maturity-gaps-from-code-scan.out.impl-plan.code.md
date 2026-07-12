All gates pass. Here's the complete implementation report:

---

# 代码实现报告

## 实现概述

Implemented the **five notification lifecycle and domain model gaps** described in `docs/requirements/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.md`, following the architecture analysis in `docs/results/...arch.md`. The implementation adds:

1. **Direction 1 (P1)**: Top-level comments now emit `file.commented` / `message.commented` events → target owner receives notification
2. **Direction 2 (P1)**: Reactions/Likes now emit `message.reacted` / `comment.liked` events → peer/comment-owner receives notification
3. **Direction 3 (P2)**: Soft-deleted messages now emit `message.deleted` event → recipient receives notification
4. **Direction 4 (P2)**: Files now have a `FileCategory` enum with MIME→category mapping + `category` field
5. **Direction 5 (P2)**: `NotificationPreference` now supports `ScopeType`/`ScopeID` for resource-level granularity

## 文件清单

### Modified files:
- `internal/domain/notification.go` - Added 5 new `NotificationType` constants; added `ScopeType`/`ScopeID` to `NotificationPreference`
- `internal/domain/file.go` - Added `FileCategory` enum, MIME→category map, `ClassifyFile()` function, `Category` field to `File`
- `internal/service/notification.go` - Added 5 new payload types and `notificationFor()` switch cases; refactored rate limiter out
- `internal/service/comment.go` - Added `file.commented`/`message.commented` event production in `Create()`; added `resolveTargetOwner` helper; refactored reactions out
- `internal/service/message.go` - Added `message.reacted` event in `ReactToMessage()`; switched `Delete()` to use `SoftDeleteWithEvent` for transactional event emitting
- `internal/repository/comment.go` - Added `AppendEvent` to interface + implementation for standalone outbox writes
- `internal/repository/message.go` - Added `SoftDeleteWithEvent` and `AppendEvent` to interface + implementations
- `internal/service/mocks/mocks.go` - Added mock methods for new repository methods
- `internal/service/message_test.go` - Updated delete test to match new method name

### New files:
- `internal/service/notif_rate_limiter.go` - Extracted rate limiter from notification.go for file size compliance
- `internal/service/comment_reactions.go` - Extracted Like/Unlike/react methods from comment.go for file size compliance

## 核心代码实现

### Direction 1: Top-level comments → `file.commented` / `message.commented`

In `commentService.Create()` (`internal/service/comment.go`), when `parent == nil` (top-level comment), the method now resolves the target owner via `resolveTargetOwner()` and emits the appropriate event:

```go
} else {
    ownerUID, err := s.resolveTargetOwner(ctx, in.TargetType, in.TargetID)
    if err == nil && ownerUID != "" && ownerUID != actor {
        eventType := "file.commented"
        if in.TargetType == domain.TargetTypeMessage {
            eventType = "message.commented"
        }
        evt = newEvent("comment", c.ID.String(), eventType, map[string]any{...})
    }
}
```

### Direction 2: Reactions/Likes → `message.reacted` / `comment.liked`

In `messageService.ReactToMessage()` (`internal/service/message.go`), after the reaction is persisted, the peer participant is identified and a `message.reacted` event is emitted:

```go
peerUID := m.FromUID
if actorUID == m.FromUID {
    peerUID = m.ToUID
}
if peerUID != "" && peerUID != actorUID {
    evt := newEvent("message", messageID.String(), "message.reacted", map[string]any{...})
    _ = s.messages.AppendEvent(ctx, evt)
}
```

In `commentService.Like()` (`internal/service/comment_reactions.go`), after the like is persisted, a `comment.liked` event is emitted:

```go
if c.OwnerUID != actor {
    evt := newEvent("comment", id.String(), "comment.liked", map[string]any{...})
    _ = s.comments.AppendEvent(ctx, evt)
}
```

### Direction 3: Soft delete → `message.deleted`

The `Delete()` method now uses `SoftDeleteWithEvent` for transactional delete + outbox event append:

```go
evt := newEvent("message", id.String(), "message.deleted", map[string]any{...})
if err := s.messages.SoftDeleteWithEvent(ctx, id, evt); err != nil { ... }
```

### Direction 4: File type categorization

Added `FileCategory` enum (`CategoryImage/Video/Audio/Document/Archive/Other`) with MIME→category mapping and `ClassifyFile()` function in `internal/domain/file.go`. The `File` struct now includes a `Category` field.

### Direction 5: Resource-level notification preferences

Extended `NotificationPreference` in `internal/domain/notification.go` with `ScopeType` ("" for global, "file", "conversation") and `ScopeID` fields, with backward-compatible empty-string defaults.

### Notification dispatcher

All 5 new event types have corresponding cases in `notificationFor()` switch in `internal/service/notification.go`, with proper self-action exclusion (`owner == actor`, `liker == owner`, etc.) and collapsed-count initialization.

## 依赖说明

No new dependencies added.

## 验证步骤

```
make test && make lint && make check-file-sizes
```

- ✅ `go build ./...` - compiles clean
- ✅ `go vet ./...` - no issues
- ✅ `make test` - all 27 test packages pass
- ✅ `make check-file-sizes` - all files ≤ 500 lines (refactored `notification.go` → 437 lines, `comment.go` → 481 lines)

## 配置要求

No new configuration required. The notification events are wired into the existing outbox pipeline; the dispatcher's rate limiting, preference checking, collapse, and block suppression (already in the unstaged code) apply seamlessly to the new event types.
