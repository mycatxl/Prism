import { Component, type ErrorInfo, type ReactNode } from "react";
import { RenderFailure } from "./RenderFailure";

type Props = { children: ReactNode };
type State = { error: Error | null };

/**
 * Last-resort boundary around the routed app.
 *
 * It stays deliberately blunt: it does not try to recover, because a render error
 * usually means the data and the view disagree and a retry would hit the same
 * wall. It reports the failure and offers a reload.
 *
 * The message names the error rather than saying "something went wrong", because
 * this panel is operated by one technical person who can act on the actual text.
 *
 * The fallback lives in its own file so this one only exports a component: a class
 * component cannot call a hook, so the fallback has to be a function component, and
 * exporting both from one module breaks fast refresh.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Prism UI error", error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return <RenderFailure error={error} />;
  }
}
