export type JsonRecord = Record<string, unknown>;

export interface PageData<T> {
  items: T[];
  nextCursor?: string;
}

export interface Profile {
  account_id?: string;
  display_name?: string;
  avatar_url?: string;
  locale?: string;
  timezone?: string;
  preferences?: JsonRecord;
}

export interface AccountView {
  account?: JsonRecord;
  profile?: Profile;
  sources?: JsonRecord[];
  memberships?: JsonRecord[];
}

export interface AggregateView {
  account?: JsonRecord;
  profile?: Profile;
  snapshots?: JsonRecord[];
  consistency?: string;
  partial?: boolean;
  source_errors?: Record<string, string>;
  stale_datasets?: string[];
}

export interface Dataset {
  name: string;
  source: string;
  pii: boolean;
}

export type AccessState = "allowed" | "denied" | "unknown";
export interface Capability {
  name: string;
  registered: boolean;
  enabled: boolean;
  authorization: AccessState;
  authorization_scope: "self" | "global";
  available: boolean;
  reason_code?: string;
}
export interface DatasetCapability extends Dataset {
  registered: boolean;
  enabled: boolean;
  authorization: AccessState;
  resource_authorization: "unknown";
  status: "fresh" | "stale" | "unavailable" | "unknown";
  fresh: boolean;
  version: number;
  generated_at?: string;
  last_synced_at?: string;
  reason_code?: string;
}
export interface CapabilityReport {
  schema_version: 1;
  account_id: string;
  tenant_id: string;
  client_id: string;
  issuer: string;
  home_region: string;
  local_region: string;
  generated_at: string;
  authorization_version: "unsupported";
  partial: boolean;
  capabilities: Capability[];
  datasets: DatasetCapability[];
}
