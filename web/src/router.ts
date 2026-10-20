import {
  onMounted,
  onUnmounted,
  provide,
  ref,
  type InjectionKey,
  type Ref,
} from "vue";

import { parseAuditNavigation, type AuditNavigation } from "./auditNavigation";

export const hashNavigationKey: InjectionKey<(route: string) => void> =
  Symbol("hashNavigation");

const prefix = "#/";

export function routeFromHash(fallback: string): string {
  const hash = window.location.hash;
  if (!hash || hash === "#" || hash === prefix) return fallback;
  const raw = hash.startsWith(prefix)
    ? hash.slice(prefix.length)
    : hash.replace(/^#/, "");
  // Query values are decoded individually, not before splitting delimiters.
  if (raw.startsWith("audit?")) return raw;
  try {
    return decodeURIComponent(raw).trim() || fallback;
  } catch {
    return fallback;
  }
}

const resourceID = /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i;

export function routeDetails(route: string): {
  page: string;
  jobID?: string;
  operationID?: string;
  auditNavigation?: AuditNavigation;
} {
  if (route.startsWith("audit?")) {
    const auditNavigation = parseAuditNavigation(route);
    return auditNavigation
      ? { page: "audit", auditNavigation }
      : { page: "overview" };
  }
  const [page, id, extra] = route.split("/");
  if (!id) return { page };
  if (extra !== undefined || !resourceID.test(id)) return { page: "overview" };
  if (page === "jobs") return { page, jobID: id };
  if (page === "operations") return { page, operationID: id };
  return { page: "overview" };
}

export function taskHref(
  page: "jobs" | "operations",
  id: string,
): string | undefined {
  return resourceID.test(id)
    ? `#/${page}/${encodeURIComponent(id)}`
    : undefined;
}

export function useHashRoute(fallback: string): {
  route: Ref<string>;
  navigate: (route: string) => void;
} {
  const route = ref(routeFromHash(fallback));
  const sync = () => {
    route.value = routeFromHash(fallback);
  };
  onMounted(() => {
    if (!window.location.hash)
      window.history.replaceState(null, "", `${prefix}${fallback}`);
    window.addEventListener("hashchange", sync);
  });
  onUnmounted(() => window.removeEventListener("hashchange", sync));
  const navigate = (next: string) => {
    // Publish to the shell before any child can adopt a different detail.
    route.value = next;
    if (routeFromHash("") !== next)
      window.location.hash = `${prefix}${next.startsWith("audit?") ? next : next.split("/").map(encodeURIComponent).join("/")}`;
  };
  provide(hashNavigationKey, navigate);
  return { route, navigate };
}
