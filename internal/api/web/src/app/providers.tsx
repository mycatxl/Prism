import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { queryClient } from "../lib/query-client";
import * as Tooltip from "@radix-ui/react-tooltip";
import { MotionConfig } from "framer-motion";

type AppProvidersProps = {
  children: ReactNode;
};

export function AppProviders({ children }: AppProvidersProps) {
  return (
    <QueryClientProvider client={queryClient}>
      <Tooltip.Provider delayDuration={350}><MotionConfig reducedMotion="user">{children}</MotionConfig></Tooltip.Provider>
    </QueryClientProvider>
  );
}
