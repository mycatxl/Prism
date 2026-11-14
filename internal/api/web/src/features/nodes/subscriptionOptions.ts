import { listSubscriptions } from "../subscriptions/api";

export type NodeSubscriptionOption = { id: string; name: string; enabled: boolean };

// Read every source page, including disabled sources, and keep only picker data.
export async function listNodeSubscriptionOptions(signal?: AbortSignal): Promise<NodeSubscriptionOption[]> {
  const sources = new Map<string, NodeSubscriptionOption>();
  let offset = 0;
  for (;;) {
    const page = await listSubscriptions({ limit: 1000, offset }, signal);
    for (const { id, name, enabled } of page.items) sources.set(id, { id, name, enabled });
    offset += page.items.length;
    if (page.items.length === 0 || offset >= page.total) return [...sources.values()];
  }
}
