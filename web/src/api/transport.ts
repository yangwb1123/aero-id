import type { JsonRecord } from "../types";

const requestTimeoutMs = 15_000;
const maxJsonResponseBytes = 2 * 1024 * 1024;

interface ErrorEnvelope {
  error?: { code?: string; message?: string; request_id?: string };
}

interface PendingResponse {
  response: Response;
  timedOut(): boolean;
  canceled(): boolean;
  finish(): void;
}

export interface RequestOptions {
  query?: Record<string, string | number | string[] | undefined>;
  body?: unknown;
  idempotencyKey?: string;
  timeoutMs?: number;
  signal?: AbortSignal;
}

export class AeroIdError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string,
  ) {
    super(message);
    this.name = "AeroIdError";
  }
}

function validateApiBase(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new AeroIdError(0, "config.invalid", "aero-id API 地址无效");
  }
  const loopback = ["localhost", "127.0.0.1", "[::1]", "::1"].includes(
    url.hostname,
  );
  if (
    (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new AeroIdError(
      0,
      "config.invalid",
      "aero-id API 地址必须使用 HTTPS，且不能包含凭据、query 或 fragment",
    );
  }
  if (!url.pathname.replace(/\/+$/, "").endsWith("/v1")) {
    throw new AeroIdError(0, "config.invalid", "aero-id API 地址必须指向 /v1");
  }
  return url.href.replace(/\/+$/, "");
}

export class AeroIdTransport {
  private readonly baseUrl: string;
  private writeEpoch?: string;
  private requestSequence = 0;
  private writeEpochSequence = 0;
  private readonly reads = new Set<AbortController>();

  constructor(
    baseUrl: string,
    private readonly accessToken: () => string,
    private readonly fetcher: typeof fetch,
  ) {
    this.baseUrl = validateApiBase(baseUrl);
  }

  cancelPendingReads(): void {
    for (const controller of this.reads) controller.abort();
    this.reads.clear();
  }

  pinWriteEpoch(region: string, epoch: number): void {
    const target = region.trim();
    if (
      !target ||
      target.includes(":") ||
      !Number.isSafeInteger(epoch) ||
      epoch < 0
    )
      throw new AeroIdError(
        0,
        "response.invalid",
        "aero-id 返回了无效的 Writer Epoch",
      );
    this.writeEpoch = `${target}:${epoch}`;
    this.writeEpochSequence = ++this.requestSequence;
  }

  async request(
    method: string,
    path: string,
    options: RequestOptions = {},
  ): Promise<unknown> {
    const pending = await this.send(method, path, options);
    try {
      const payload = await this.readJSON(pending.response);
      if (method.toUpperCase() === "GET")
        this.rejectStaleRead(pending.response);
      return payload;
    } catch (reason) {
      throw this.responseReadError(
        reason,
        pending.timedOut(),
        pending.canceled(),
      );
    } finally {
      pending.finish();
    }
  }

  async download(
    path: string,
    options: RequestOptions,
    maxBytes: number,
  ): Promise<Blob> {
    const pending = await this.send("GET", path, options);
    try {
      const blob = await this.readBlob(pending.response, maxBytes);
      this.rejectStaleRead(pending.response);
      return blob;
    } catch (reason) {
      throw this.responseReadError(
        reason,
        pending.timedOut(),
        pending.canceled(),
      );
    } finally {
      pending.finish();
    }
  }

  private async send(
    method: string,
    path: string,
    options: RequestOptions,
  ): Promise<PendingResponse> {
    const token = this.accessToken().trim();
    if (!token) throw new AeroIdError(401, "auth.required", "登录状态已失效");
    const sequence = ++this.requestSequence;
    const pending = await this.perform(
      this.buildURL(path, options.query),
      method,
      this.buildHeaders(token, options),
      options.body,
      options.timeoutMs,
      options.signal,
    );
    const responseEpoch = pending.response.headers.get("X-Aero-Write-Epoch");
    if (responseEpoch && sequence > this.writeEpochSequence) {
      this.writeEpoch = responseEpoch;
      this.writeEpochSequence = sequence;
    }
    return pending;
  }

  private buildURL(path: string, query: RequestOptions["query"]): URL {
    const url = new URL(
      `${this.baseUrl.replace(/\/+$/, "")}/${path.replace(/^\/+/, "")}`,
    );
    for (const [name, raw] of Object.entries(query ?? {})) {
      if (raw === undefined || raw === "") continue;
      for (const value of Array.isArray(raw) ? raw : [raw])
        url.searchParams.append(name, String(value));
    }
    return url;
  }

  private buildHeaders(token: string, options: RequestOptions): Headers {
    const headers = new Headers({
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      "X-Request-ID": crypto.randomUUID(),
    });
    if (options.body !== undefined)
      headers.set("Content-Type", "application/json");
    if (options.idempotencyKey)
      headers.set("Idempotency-Key", options.idempotencyKey);
    if (this.writeEpoch) headers.set("X-Aero-Write-Epoch", this.writeEpoch);
    return headers;
  }

  private async perform(
    url: URL,
    method: string,
    headers: Headers,
    body: unknown,
    timeoutMs = requestTimeoutMs,
    signal?: AbortSignal,
  ): Promise<PendingResponse> {
    if (signal?.aborted) throw canceledRequest();
    const controller = new AbortController();
    const isRead = method.toUpperCase() === "GET";
    if (isRead) this.reads.add(controller);
    const cancel = () => controller.abort();
    signal?.addEventListener("abort", cancel, { once: true });
    let timeoutTriggered = false;
    const timeout = window.setTimeout(() => {
      if (controller.signal.aborted) return;
      timeoutTriggered = true;
      controller.abort();
    }, timeoutMs);
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timeout);
      signal?.removeEventListener("abort", cancel);
      this.reads.delete(controller);
    };
    try {
      const response = await this.fetcher(url, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: "omit",
        signal: controller.signal,
      });
      return {
        response,
        timedOut: () => timeoutTriggered,
        canceled: () => controller.signal.aborted && !timeoutTriggered,
        finish,
      };
    } catch {
      finish();
      if (timeoutTriggered)
        throw new AeroIdError(0, "network.timeout", "aero-id 请求超时");
      if (controller.signal.aborted) throw canceledRequest();
      throw new AeroIdError(0, "network.unavailable", "无法连接 aero-id");
    }
  }

  private async readBlob(response: Response, maxBytes: number): Promise<Blob> {
    if (!response.ok) {
      await this.readJSON(response);
      throw new AeroIdError(response.status, "request.failed", "下载失败");
    }
    const bytes = await this.readBytes(
      response,
      maxBytes,
      "导出文件超过浏览器下载上限",
    );
    return new Blob([bytes.buffer], {
      type: response.headers.get("Content-Type") ?? "application/octet-stream",
    });
  }

  private async readJSON(response: Response): Promise<unknown> {
    if (response.status === 204) return undefined;
    const bytes = await this.readBytes(
      response,
      maxJsonResponseBytes,
      "aero-id 响应过大",
    );
    const text = new TextDecoder().decode(bytes);
    let payload: unknown;
    try {
      payload = text ? JSON.parse(text) : {};
    } catch {
      throw new AeroIdError(
        response.status,
        "response.invalid",
        "aero-id 返回了无效响应",
      );
    }
    if (!response.ok) {
      const envelope = isRecord(payload) ? (payload as ErrorEnvelope) : {};
      throw new AeroIdError(
        response.status,
        envelope.error?.code ?? "request.failed",
        envelope.error?.message ?? "请求失败",
        envelope.error?.request_id ??
          response.headers.get("X-Request-ID") ??
          undefined,
      );
    }
    return payload;
  }

  private async readBytes(
    response: Response,
    maxBytes: number,
    tooLargeMessage: string,
  ): Promise<Uint8Array<ArrayBuffer>> {
    const declaredLength = Number(response.headers.get("Content-Length"));
    if (Number.isFinite(declaredLength) && declaredLength > maxBytes) {
      throw new AeroIdError(
        response.status,
        "response.too_large",
        tooLargeMessage,
      );
    }
    if (!response.body) return new Uint8Array();

    const reader = response.body.getReader();
    const chunks: Uint8Array[] = [];
    let total = 0;
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        total += value.byteLength;
        if (total > maxBytes) {
          await reader.cancel();
          throw new AeroIdError(
            response.status,
            "response.too_large",
            tooLargeMessage,
          );
        }
        chunks.push(value);
      }
    } finally {
      reader.releaseLock();
    }
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return bytes;
  }

  private rejectStaleRead(response: Response): void {
    if (response.headers.get("X-Aero-Stale-Read")?.toLowerCase() !== "true")
      return;
    throw new AeroIdError(
      response.status,
      "region.stale_read",
      "当前区域无法确认最近一次写入已同步；请刷新，若仍出现请联系管理员切换到账户主区域",
      response.headers.get("X-Request-ID") ?? undefined,
    );
  }

  private responseReadError(
    reason: unknown,
    timedOut: boolean,
    canceled: boolean,
  ): Error {
    if (reason instanceof AeroIdError) return reason;
    if (timedOut)
      return new AeroIdError(0, "network.timeout", "aero-id 请求超时");
    if (canceled) return canceledRequest();
    return new AeroIdError(0, "network.unavailable", "无法读取 aero-id 响应");
  }
}

function canceledRequest(): AeroIdError {
  return new AeroIdError(
    0,
    "request.canceled",
    "请求已取消；已提交命令的结果仍需查询核实",
  );
}

export function requireDataEnvelope(value: unknown): JsonRecord {
  if (!isRecord(value) || !isRecord(value.data)) {
    throw new AeroIdError(
      0,
      "response.invalid",
      "aero-id 成功响应缺少有效的 data 对象",
    );
  }
  return value.data;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
