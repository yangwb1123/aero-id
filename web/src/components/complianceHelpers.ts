const rfc3339 =
  /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])T([01]\d|2[0-3]):([0-5]\d):([0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;

function validCalendarDate(parts: RegExpMatchArray): boolean {
  const [year, month, day, hour, minute, second] = parts
    .slice(1, 7)
    .map(Number);
  const calendar = new Date(0);
  calendar.setUTCFullYear(year, month - 1, day);
  calendar.setUTCHours(hour, minute, second, 0);
  return (
    calendar.getUTCFullYear() === year &&
    calendar.getUTCMonth() === month - 1 &&
    calendar.getUTCDate() === day
  );
}

export function normalizeHoldExpiry(
  value: string,
  now = Date.now(),
): string | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const parts = trimmed.match(rfc3339);
  if (!parts) throw new Error("到期时间必须是带时区的 RFC3339 时间");
  if (!validCalendarDate(parts)) throw new Error("到期时间无效");
  const timestamp = Date.parse(trimmed);
  if (!Number.isFinite(timestamp)) throw new Error("到期时间无效");
  if (timestamp <= now) throw new Error("到期时间必须晚于当前时间");
  return new Date(timestamp).toISOString();
}
