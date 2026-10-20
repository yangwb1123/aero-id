import { inject, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { hashNavigationKey, taskHref } from "../router";

// The URL contains only a resource pointer, never a plan, token or command key.
export function useTaskNavigation(
  page: "jobs" | "operations",
  id: () => string | undefined,
) {
  const navigate = inject(hashNavigationKey, undefined);
  const valid = (value: string | undefined) =>
    value && taskHref(page, value) ? value : undefined;
  const selected = ref(valid(id()));
  const detailRoot = ref<HTMLElement>();
  const focusDetail = () => {
    if (selected.value) detailRoot.value?.focus();
  };
  onMounted(focusDetail);
  watch(selected, focusDetail, { flush: "post" });
  watch(id, (value) => {
    selected.value = valid(value);
  });
  let active = true;
  onBeforeUnmount(() => {
    active = false;
  });
  function select(value: string | undefined): void {
    if (!active || (value !== undefined && !valid(value))) return;
    const route = value === undefined ? page : `${page}/${value}`;
    if (navigate) {
      // In the shell, only its admitted route prop adopts the selection.
      navigate(route);
    } else {
      // Standalone embedding retains local selection, with the same safe URL.
      selected.value = value;
      window.location.hash = `#/${route}`;
    }
  }
  return { selected, detailRoot, select, close: () => select(undefined) };
}
