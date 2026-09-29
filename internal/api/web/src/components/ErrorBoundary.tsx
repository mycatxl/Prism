import { AlertTriangle, RefreshCw } from "lucide-react";
import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui/Button";

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

    return (
      <main className="grid min-h-dvh place-items-center bg-paper px-6" role="alert">
        <div className="w-full max-w-md">
          <div className="flex items-start gap-2.5">
            <AlertTriangle size={20} className="mt-0.5 shrink-0 text-alert" />
            <div className="min-w-0">
              <h1 className="text-base font-semibold">界面渲染失败</h1>
              <p className="mt-1 text-xs leading-relaxed text-ink-soft">
                这个页面在渲染时出错，重新加载通常就能恢复。如果反复出现，请把下面的信息保留下来。
              </p>
            </div>
          </div>

          <pre className="readout mt-3 max-h-40 overflow-auto rounded-panel border border-rule bg-paper-raised p-2.5 text-xs text-alert whitespace-pre-wrap">
            {error.message}
          </pre>

          <Button
            variant="secondary"
            size="sm"
            className="mt-3"
            onClick={() => window.location.reload()}
          >
            <RefreshCw size={14} />
            重新加载
          </Button>
        </div>
      </main>
    );
  }
}