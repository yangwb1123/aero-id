import { onMounted, onUnmounted, ref, type Ref } from "vue";

const prefix = "#/";

export function routeFromHash(fallback: string): string {
  const hash = window.location.hash;
  if (!hash || hash === "#" || hash === prefix) return fallback;
  const raw = hash.startsWith(prefix)
    ? hash.slice(prefix.length)
    : hash.replace(/^#/, "");
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
} {
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
    if (routeFromHash("") !== next)
      window.location.hash = `${prefix}${next.split("/").map(encodeURIComponent).join("/")}`;
  };
  return { route, navigate };
}
