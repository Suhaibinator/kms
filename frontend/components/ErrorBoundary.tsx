import { Component, type ErrorInfo, type ReactNode } from "react";
import { LogoMark } from "@/components/LogoMark";
import { Button, ButtonLink } from "@/components/ui/button";

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
          {/* The exception text is for whoever files the bug, not the operator
              reading the card, so it stays folded. */}
          <details className="advanced-panel mb-4">
            <summary>Technical details</summary>
            <div className="advanced-panel-content">
              <pre className="json-block">{error.message || String(error)}</pre>
            </div>
          </details>
          <div className="flex gap-2">
            {/* A class component has no router, and a full reload is the surer
                way back from a render tree that has already failed once; the
                overview link is the escape that keeps the session. */}
            <Button className="flex-1" onClick={() => window.location.reload()} type="button">
              Reload
            </Button>
            <ButtonLink href="/" variant="outline" className="flex-1">
              Back to overview
            </ButtonLink>
          </div>
        </div>
      </main>
    );
  }
}

export default ErrorBoundary;
