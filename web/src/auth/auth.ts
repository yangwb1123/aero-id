import { reactive, inject, provide, type InjectionKey } from "vue";
import type { PlatformConfig } from "../config";
import { OidcClient, type OidcSession } from "./oidc";

export type AuthStatus = "loading" | "anonymous" | "authenticated" | "error";

export interface AuthState {
  status: AuthStatus;
  session?: OidcSession;
  error?: string;
  login(): Promise<void>;
  logout(): Promise<void>;
}

export const authKey: InjectionKey<AuthState> = Symbol("aero-id-auth");

export function createAuthState(config: PlatformConfig): AuthState {
  const client = new OidcClient(config);
  const state = reactive<AuthState>({
    status: client.hasCallback() ? "loading" : "anonymous",
    session: undefined,
    error: undefined,
    login: async () => {
      state.error = undefined;
      await client.beginLogin(window.location.hash || "#/overview");
    },
    logout: async () => {
      const idToken = state.session?.idToken;
      state.session = undefined;
      state.error = undefined;
      state.status = "anonymous";
      try {
        await client.logout(idToken);
      } catch (reason) {
        state.error = reason instanceof Error ? reason.message : "退出失败";
      }
    },
  });

  if (client.hasCallback()) {
    void client.completeLogin().then(
      (session) => {
        state.session = session;
        state.status = "authenticated";
        if (session.returnTo)
          window.location.hash = session.returnTo.replace(/^#/, "");
        armExpiry(state);
      },
      (reason: unknown) => {
        state.error = reason instanceof Error ? reason.message : "登录失败";
        state.status = "error";
      },
    );
  }

  return state;
}

let expiryTimer: number | undefined;
function armExpiry(state: AuthState): void {
  if (expiryTimer !== undefined) window.clearTimeout(expiryTimer);
  const remaining = Math.max(
    0,
    (state.session?.expiresAt ?? Date.now()) - Date.now(),
  );
  expiryTimer = window.setTimeout(() => {
    state.session = undefined;
    state.status = "anonymous";
    state.error = "登录已过期，请重新登录";
  }, remaining);
}

export function provideAuth(state: AuthState): void {
  provide(authKey, state);
}

export function useAuth(): AuthState {
  const state = inject(authKey);
  if (!state) throw new Error("useAuth must be used inside the auth provider");
  return state;
}
