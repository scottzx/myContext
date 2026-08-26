import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

// Catches a render-time crash anywhere below it (e.g. a Go DTO field renamed
// out from under types.ts - see internal/ops/contract_test.go for the guard
// against that) so the dashboard degrades to a visible message instead of
// React unmounting the whole tree to a blank page.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("dashboard crashed:", error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return <div className="error-banner">{this.state.error.message}</div>;
    }
    return this.props.children;
  }
}
