import type { QueryClient } from "@tanstack/react-query";

// A band change affects every assessment projection and cached scope preview.
export async function invalidatePurityQueries(client: QueryClient) {
  await Promise.all([
    "nodes", "node", "quality", "intel", "intel-node", "intel-ip",
    "platforms", "platform", "platform-scope-preview", "platform-node-facets",
  ].map(key => client.invalidateQueries({ queryKey: [key] })));
}
