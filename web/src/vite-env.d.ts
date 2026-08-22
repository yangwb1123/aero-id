/// <reference types="vite/client" />

interface AeroPlatformRuntimeConfig {
  snaplinkIssuer?: string;
  snaplinkHostedLoginUrl?: string;
  snaplinkClientId?: string;
  snaplinkResource?: string;
  snaplinkScopes?: string[];
  redirectUri?: string;
  aeroIdApiBase?: string;
  auditConsoleUrl?: string;
  aeroImConsoleUrl?: string;
  aeroVaultConsoleUrl?: string;
}

interface Window {
  __AERO_PLATFORM_CONFIG__?: AeroPlatformRuntimeConfig;
}
