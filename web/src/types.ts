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
