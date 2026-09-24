export type SubscriptionParseReason = {
  reason: string;
  count: number;
  detail?: string;
  sample_names: string[];
  sample_types?: string[];
  samples_truncated: boolean;
};

export type SubscriptionParseReport = {
  total: number;
  imported: number;
  skipped: number;
  skipped_overflow?: number;
  reasons: SubscriptionParseReason[];
  reasons_overflow?: number;
};

export type SubscriptionSkippedNode = {
  name: string;
  type: string;
  source: string;
  reason: string;
  detail: string;
};

export type SubscriptionParseReportDetail = {
  subscription_id: string;
  parsed: boolean;
  truncated: boolean;
  original_bytes?: number;
  summary: SubscriptionParseReport;
  stats: {
    total: number;
    imported: number;
    skipped: number;
    skipped_overflow?: number;
    by_engine?: Record<string, number>;
    by_protocol?: Record<string, number>;
  };
  skipped: SubscriptionSkippedNode[];
};

export type Subscription = {
  id: string;
  name: string;
  source_type: "remote" | "local";
  url: string;
  content: string;
  update_interval: string;
  node_count: number;
  healthy_node_count: number;
  ephemeral: boolean;
  incremental_alive_nodes: boolean;
  ephemeral_node_evict_delay: string;
  auto_intel: boolean;
  parse_report?: SubscriptionParseReport | null;
  enabled: boolean;
  created_at: string;
  last_checked?: string;
  last_updated?: string;
  last_error?: string;
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type SubscriptionCreateInput = {
  name: string;
  source_type?: "remote" | "local";
  url?: string;
  content?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  incremental_alive_nodes?: boolean;
  ephemeral_node_evict_delay?: string;
  auto_intel?: boolean;
};

export type SubscriptionUpdateInput = {
  name?: string;
  url?: string;
  content?: string;
  update_interval?: string;
  enabled?: boolean;
  ephemeral?: boolean;
  incremental_alive_nodes?: boolean;
  ephemeral_node_evict_delay?: string;
  auto_intel?: boolean;
};
