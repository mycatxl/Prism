type ControlToastContainerProps = {
  toasts?: readonly unknown[];
  onDismiss?: (id: number) => void;
};

/**
 * Kept as a staged compatibility export while page-level toast mounts are
 * removed. The actual region is owned by AppProviders and HeroUI's queue.
 */
export function ControlToastContainer(_props: ControlToastContainerProps) {
  void _props;
  return null;
}
