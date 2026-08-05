-- 000029_conversation_indexes.up.sql
-- Adds an expression index for efficient conversation aggregation queries.
-- The DISTINCT ON pattern in ListConversations sorts by the conversation key
-- (LEAST(from_uid, to_uid) || ':' || GREATEST(from_uid, to_uid)) and then by
-- created_at DESC. Without this index, PostgreSQL must seq-scan and sort all
-- messages for the actor, which degrades for users with many conversations.
--
-- The index covers both the conv_key equality/sort and the created_at DESC
-- sort within each conversation group. It only indexes live (non-deleted)
-- rows to keep size manageable.

CREATE INDEX IF NOT EXISTS idx_messages_conversation
ON messages (
    LEAST(from_uid, to_uid),
    GREATEST(from_uid, to_uid),
    created_at DESC
) WHERE deleted_at IS NULL;

-- Also add a simple index for the CountUnreadFrom query (to_uid, from_uid, read_at).
CREATE INDEX IF NOT EXISTS idx_messages_unread_from
ON messages (to_uid, from_uid)
WHERE read_at IS NULL AND deleted_at IS NULL;
