import { onMounted, onUnmounted, ref, type Ref } from "vue";

const prefix = "#/";

export function routeFromHash(fallback: string): string {
  const hash = window.location.hash;
  if (!hash || hash === "#" || hash === prefix) return fallback;
  const raw = hash.startsWith(prefix)
    ? hash.slice(prefix.length)
    : hash.replace(/^#/, "");
  return decodeURIComponent(raw).trim() || fallback;
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
      window.location.hash = `${prefix}${encodeURIComponent(next)}`;
  };
  return { route, navigate };
}
