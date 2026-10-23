import { useQuery } from "@tanstack/react-query";
import { getUserBalance } from "./api";

const LEDGER_LIMIT = 50;

/** The one cache entry for a user's balance view; every reader shares it. */
export function adminUserBalanceQueryKey(userId: string) {
  return ["admin-user-balance", userId] as const;
}

/** The balance view behind the summary readout and the ledger panel. */
export function useUserBalance(userId: string) {
  return useQuery({
    queryKey: adminUserBalanceQueryKey(userId),
    queryFn: () => getUserBalance(userId, LEDGER_LIMIT),
    enabled: Boolean(userId),
  });
}
