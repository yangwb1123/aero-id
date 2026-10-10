import { inject, type InjectionKey } from "vue";

// Scope comes from the verified /me response plus public deployment/client
// identifiers. It isolates browser receipts; it is never an authorization grant.
export const accountScopeKey: InjectionKey<() => string | undefined> =
  Symbol("account-scope");
export const useAccountScope = (): (() => string | undefined) | undefined =>
  inject(accountScopeKey, undefined);
