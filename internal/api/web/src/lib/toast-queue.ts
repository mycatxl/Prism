import { ToastQueue } from "@heroui/react";
import type { ToastContentValue } from "@heroui/react";

export const appToastQueue = new ToastQueue<ToastContentValue>({
  exitDuration: 340,
  maxVisibleToasts: 4,
});
