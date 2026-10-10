import { inject, type InjectionKey } from "vue";
import type { CapabilityReport } from "../types";

export const capabilityContextKey: InjectionKey<
  () => CapabilityReport | undefined
> = Symbol("capabilities");
export const useCapabilities = ():
  (() => CapabilityReport | undefined) | undefined =>
  inject(capabilityContextKey, undefined);
