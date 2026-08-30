import { Component, type ErrorInfo, type ReactNode } from "react";

// Catches a render-time exception in whichever screen is active so one
// broken view degrades to a message instead of blanking the whole
// console (audit P2.4: "no error boundary"). The key on the boundary in
// App.tsx resets it when the operator switches screen.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("screen crashed", error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="panel" role="alert">
          <h3>This screen hit an error</h3>
          <p className="dim" style={{ fontFamily: "var(--font-mono)", fontSize: "0.8rem" }}>
            {this.state.error.message}
          </p>
          <button className="btn" onClick={() => this.setState({ error: null })}>
            Try again
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
