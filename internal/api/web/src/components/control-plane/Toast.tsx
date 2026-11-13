type ControlToastContainerProps = {
  toasts?: readonly unknown[];
  onDismiss?: (id: number) => void;
};

/** The global toast viewport is mounted once by AppProviders. */
export function ControlToastContainer(_props: ControlToastContainerProps) {
  void _props;
  return null;
}
