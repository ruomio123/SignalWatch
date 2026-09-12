export type Language = "zh" | "en";
export interface Profile {
  id: number;
  email: string;
  timezone: string;
  digest_time: string;
  max_items_per_digest: number;
  ai_enabled: boolean;
  ai_language: Language;
  status: string;
  created_at: string;
}
export interface Source {
  id: number;
  name: string;
  kind: string;
  allowed_categories: string[];
}
export interface Page<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
}
export interface Subscription {
  id: number;
  name: string;
  objective: string | null;
  source: Source;
  enabled: boolean;
  version: number;
  max_items_per_digest: number;
  digest_ai_enabled: boolean;
  digest_ai_language: Language;
  rules: { category: string; keywords: string[] };
  backfill: { state: string; processed: number; matched: number };
}
export interface SubscriptionInput {
  source_id?: number;
  name: string;
  objective: string | null;
  enabled: boolean;
  max_items_per_digest: number;
  digest_ai_enabled: boolean;
  digest_ai_language: Language;
  rules: { category: string; keywords: string[] };
}
export interface Match {
  subscription_id: number;
  subscription_name: string;
  category: string;
  subscription_active: boolean;
  matched_keywords: string[];
}
export interface Paper {
  id: number;
  title: string;
  abstract: string;
  comments: string;
  authors: string[];
  categories: string[];
  arxiv_url: string;
  pdf_url: string;
  published_at: string;
  first_seen_at: string;
  matches: Match[];
}
export interface Configuration {
  id?: string;
  name?: string;
  created_at?: string;
  last_used_at?: string;
  last_tested_at?: string;
  is_default?: boolean;
  configured: boolean;
  usable: boolean;
  generation?: string;
  version?: number;
  provider?: string;
  model?: string;
  masked_key?: string;
  unusable_reason?: string;
}
export interface Provider {
  id: string;
  name: string;
  models: { id: string; name: string; default: boolean }[];
}
export interface Usage {
  day: string;
  feature: string;
  calls: number;
  succeeded: number;
  failed: number;
  unknown?: number;
  input_tokens: number;
  output_tokens: number;
  usage_missing: number;
}
export interface Summary {
  summary: string;
  contributions: string[];
  method: string;
  applications: { text: string; inferred: boolean }[];
  evidence: { field: string; quote: string }[];
  limitations: string;
}
export interface SummaryResponse {
  items: {
    state: string;
    language: Language;
    content?: Summary;
    failure_code?: string;
    retry_at?: string;
  }[];
  provider?: string;
  model?: string;
}

export interface FeatureUsage {
  feature: string;
  daily_limit: number;
  calls: number;
  remaining?: number;
  reset_at?: string;
  min_interval_seconds: number;
}
export interface AICall {
  id: string;
  feature: string;
  provider: string;
  model: string;
  status: string;
  failure_code?: string;
  created_at: string;
  duration_ms: number;
  usage_known: boolean;
}
