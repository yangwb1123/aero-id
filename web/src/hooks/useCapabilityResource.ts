import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "./useAsyncResource";

// Metadata GETs only. While checking, on failure, or after the local freshness
// budget, previous grants are unavailable. This does not assert a policy version.
export function useCapabilityResource(
  client: AeroIdClient,
  issuer: string,
  clientID: string,
) {
  const inFlight = ref(0);
  const resource = useAsyncResource(async (signal) => {
    inFlight.value += 1;
    try {
      const report = await client.getCapabilities(signal);
      if (report.issuer !== issuer || report.client_id !== clientID)
        throw new Error("能力报告与当前登录客户端不一致；不会推断权限");
      return report;
    } finally {
      inFlight.value -= 1;
    }
  });
  let refreshRequest: Promise<void> | undefined;
  const publishing = ref(false);
  const refresh = (): Promise<void> => {
    if (refreshRequest) return refreshRequest;
    // Keep old guidance unavailable until AsyncResource publishes data/error,
    // not merely until the authority callback's finally block has run.
    publishing.value = true;
    refreshRequest = resource.refresh().finally(() => {
      publishing.value = false;
      refreshRequest = undefined;
    });
    return refreshRequest;
  };
  const refreshVisible = () => {
    if (!document.hidden) void refresh();
  };
  let timer: ReturnType<typeof setInterval> | undefined;
  onMounted(() => {
    timer = setInterval(refreshVisible, 60_000);
    window.addEventListener("focus", refreshVisible);
    document.addEventListener("visibilitychange", refreshVisible);
  });
  onBeforeUnmount(() => {
    clearInterval(timer);
    window.removeEventListener("focus", refreshVisible);
    document.removeEventListener("visibilitychange", refreshVisible);
  });
  const checking = computed(
    () => publishing.value || inFlight.value > 0 || resource.loading,
  );
  const report = () => {
    const value = resource.data;
    const age = Date.now() - Date.parse(value?.generated_at ?? "");
    return !checking.value &&
      !resource.error &&
      Number.isFinite(age) &&
      age >= -30_000 &&
      age < 120_000
      ? value
      : undefined;
  };
  return { resource, checking, refresh, report };
}
