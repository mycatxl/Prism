import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { Button } from "./ui/Button";

type Props = { children: ReactNode };
type State = { error: Error | null };

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Prism UI error", error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main className="error-screen" role="alert">
        <AlertTriangle size={26} />
        <h1>页面暂时无法显示</h1>
        <p>页面发生了意外错误，请重新加载后继续。</p>
        <Button onClick={() => window.location.reload()}>
          <RefreshCw size={15} />
          重新加载
        </Button>
      </main>
    );
  }
}
