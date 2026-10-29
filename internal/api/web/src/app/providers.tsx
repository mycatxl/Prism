import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { ToastProvider } from "@heroui/react";
import { MotionConfig } from "framer-motion";
import { queryClient } from "../lib/query-client";
import { appToastQueue } from "../lib/toast-queue";

type AppProvidersProps = {
  children: ReactNode;
};

export function AppProviders({ children }: AppProvidersProps) {
  return (
    <QueryClientProvider client={queryClient}>
      <MotionConfig reducedMotion="user">
        <>
          {children}
          <ToastProvider queue={appToastQueue} />
        </>
      </MotionConfig>
    </QueryClientProvider>
  );
}
