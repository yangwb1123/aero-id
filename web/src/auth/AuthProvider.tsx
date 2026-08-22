import * as React from "react";
import type { PlatformConfig } from "../config";
import { OidcClient, type OidcSession } from "./oidc";

type AuthStatus = "loading" | "anonymous" | "authenticated" | "error";

interface AuthContextValue {
  status: AuthStatus;
  session?: OidcSession;
  error?: string;
  login(): Promise<void>;
  logout(): Promise<void>;
}

const AuthContext = React.createContext<AuthContextValue | null>(null);

export function AuthProvider({
  config,
  children,
}: {
  config: PlatformConfig;
  children: React.ReactNode;
}): React.ReactElement {
  const clientRef = React.useRef<OidcClient>();
  if (!clientRef.current) clientRef.current = new OidcClient(config);
  const client = clientRef.current;
  const [status, setStatus] = React.useState<AuthStatus>(() =>
    client.hasCallback() ? "loading" : "anonymous",
  );
  const [session, setSession] = React.useState<OidcSession>();
  const [error, setError] = React.useState<string>();

  React.useEffect(() => {
    if (!client.hasCallback()) return;
    let active = true;
    client.completeLogin().then(
      (next) => {
        if (!active) return;
        setSession(next);
        setStatus("authenticated");
        if (next.returnTo)
          window.location.hash = next.returnTo.replace(/^#/, "");
      },
      (reason: unknown) => {
        if (!active) return;
        setError(reason instanceof Error ? reason.message : "登录失败");
        setStatus("error");
      },
    );
    return () => {
      active = false;
    };
  }, [client]);

  React.useEffect(() => {
    if (!session) return;
    const remaining = Math.max(0, session.expiresAt - Date.now());
    const timer = window.setTimeout(() => {
      setSession(undefined);
      setStatus("anonymous");
      setError("登录已过期，请重新登录");
    }, remaining);
    return () => window.clearTimeout(timer);
  }, [session]);

  const value = React.useMemo<AuthContextValue>(
    () => ({
      status,
      session,
      error,
      login: async () => {
        setError(undefined);
        await client.beginLogin(window.location.hash || "#/overview");
      },
      logout: async () => {
        const idToken = session?.idToken;
        setSession(undefined);
        setError(undefined);
        setStatus("anonymous");
        try {
          await client.logout(idToken);
        } catch (reason) {
          setError(reason instanceof Error ? reason.message : "退出失败");
        }
      },
    }),
    [client, error, session, status],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const value = React.useContext(AuthContext);
  if (!value) throw new Error("useAuth must be used inside AuthProvider");
  return value;
}
