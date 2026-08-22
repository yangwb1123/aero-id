const requestTimeoutMs = 15_000;
const maxJsonResponseBytes = 2 * 1024 * 1024;

interface ErrorEnvelope {
  error?: { code?: string; message?: string; request_id?: string };
}

interface PendingResponse {
  response: Response;
  timedOut(): boolean;
  finish(): void;
}

export interface RequestOptions {
  query?: Record<string, string | number | string[] | undefined>;
  body?: unknown;
  idempotencyKey?: string;
  timeoutMs?: number;
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

export class AeroIdTransport {
  private writeEpoch?: string;

  constructor(
    private readonly baseUrl: string,
    private readonly accessToken: () => string,
    private readonly fetcher: typeof fetch,
  ) {}

  async request(
    method: string,
    path: string,
    options: RequestOptions = {},
  ): Promise<unknown> {
    const pending = await this.send(method, path, options);
    try {
      return await this.readJSON(pending.response);
    } catch (reason) {
      throw this.responseReadError(reason, pending.timedOut());
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
      return await this.readBlob(pending.response, maxBytes);
    } catch (reason) {
      throw this.responseReadError(reason, pending.timedOut());
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
    const pending = await this.perform(
      this.buildURL(path, options.query),
      method,
      this.buildHeaders(token, options),
      options.body,
      options.timeoutMs,
    );
    this.writeEpoch =
      pending.response.headers.get("X-Aero-Write-Epoch") ?? this.writeEpoch;
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
  ): Promise<PendingResponse> {
    const controller = new AbortController();
    let timeoutTriggered = false;
    const timeout = window.setTimeout(() => {
      timeoutTriggered = true;
      controller.abort();
    }, timeoutMs);
    let finished = false;
    const finish = () => {
      if (finished) return;
      finished = true;
      window.clearTimeout(timeout);
    };
    try {
      const response = await this.fetcher(url, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: "omit",
        signal: controller.signal,
      });
      return { response, timedOut: () => timeoutTriggered, finish };
    } catch {
      finish();
      if (timeoutTriggered)
        throw new AeroIdError(0, "network.timeout", "aero-id 请求超时");
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

  private responseReadError(reason: unknown, timedOut: boolean): Error {
    if (reason instanceof AeroIdError) return reason;
    if (timedOut)
      return new AeroIdError(0, "network.timeout", "aero-id 请求超时");
    return new AeroIdError(0, "network.unavailable", "无法读取 aero-id 响应");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
