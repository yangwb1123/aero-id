import { inject, type InjectionKey } from "vue";
import type { CapabilityReport } from "../types";

export const capabilityContextKey: InjectionKey<
  () => CapabilityReport | undefined
> = Symbol("capabilities");
export const useCapabilities = ():
  (() => CapabilityReport | undefined) | undefined =>
  inject(capabilityContextKey, undefined);

export const capabilityRefreshKey: InjectionKey<() => Promise<void>> =
  Symbol("capability-refresh");
export const useRefreshCapabilities = (): (() => Promise<void>) | undefined =>
  inject(capabilityRefreshKey, undefined);

export function refreshAfterAuthorizationError(
  reason: unknown,
  refresh: (() => Promise<void>) | undefined,
): void {
  if (!refresh || !reason || typeof reason !== "object") return;
  const error = reason as { status?: number; code?: string };
  if (
    error.status === 401 ||
    error.status === 403 ||
    /authorization|auth\.|forbidden|permission/.test(error.code ?? "")
  )
    void refresh().catch(() => {
      /* Unknown report remains unavailable, no write retry. */
    });
}
