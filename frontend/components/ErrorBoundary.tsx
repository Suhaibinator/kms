import { Component, type ErrorInfo, type ReactNode } from "react";
import { LogoMark } from "@/components/LogoMark";
import { Button } from "@/components/ui/button";

interface ErrorBoundaryProps {
  /**
   * Changing this clears a caught error, so navigating away from a crashed page
   * does not strand the visitor on the fallback card. It resets state rather
   * than remounting: a `key` would tear down the page subtree on every shallow
   * URL write, and several pages keep their filters in the query string.
   */
  resetKey?: string;
  children: ReactNode;
}

interface ErrorBoundaryState {
  error: Error | null;
  resetKey: string | undefined;
}

/**
 * Catches a render-time crash so one broken page does not blank the whole
 * console.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { error: null, resetKey: props.resetKey };
  }

  static getDerivedStateFromError(error: Error): Partial<ErrorBoundaryState> {
    return { error };
  }

  static getDerivedStateFromProps(
    props: ErrorBoundaryProps,
    state: ErrorBoundaryState,
  ): Partial<ErrorBoundaryState> | null {
    if (props.resetKey === state.resetKey) return null;
    return { error: null, resetKey: props.resetKey };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // There is no server runtime to report to; the browser console is all a
    // developer gets, so make sure the component stack reaches it.
    console.error("Unhandled error while rendering a page", error, info.componentStack);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <main className="auth-wrap">
        <div className="auth-card">
          <div className="auth-brand">
            <LogoMark />
            <div>
              <h1 className="auth-title">This page hit an unexpected error</h1>
              <div className="faint text-sm">The rest of the console still works.</div>
            </div>
          </div>
          <pre className="json-block mb-4">{error.message || String(error)}</pre>
          {/* A class component has no router, and a full reload is the surer
              way back from a render tree that has already failed once. */}
          <Button className="w-full" onClick={() => window.location.reload()} type="button">
            Reload
          </Button>
        </div>
      </main>
    );
  }
}

export default ErrorBoundary;
