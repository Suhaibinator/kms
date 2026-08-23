import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ErrorBoundary } from "@/components/ErrorBoundary";

function Boom({ explode }: { explode: boolean }): React.ReactNode {
  if (explode) throw new Error("state of the union is broken");
  return <p>page content</p>;
}

/** Drives both the thrown error and the reset key from outside the boundary. */
function Harness({ initialRoute = "/a" }: { initialRoute?: string }) {
  const [explode, setExplode] = useState(true);
  const [route, setRoute] = useState(initialRoute);
  return (
    <>
      <button type="button" onClick={() => setExplode(false)}>
        stop throwing
      </button>
      <button type="button" onClick={() => setRoute("/b")}>
        navigate
      </button>
      <ErrorBoundary resetKey={route}>
        <Boom explode={explode} />
      </ErrorBoundary>
    </>
  );
}

describe("ErrorBoundary", () => {
  beforeEach(() => {
    // React logs the caught error itself; the boundary adds its own line. Both
    // are noise here, and the assertions below cover the behaviour.
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  it("shows the error instead of a blank page", () => {
    render(
      <ErrorBoundary>
        <Boom explode />
      </ErrorBoundary>,
    );

    expect(screen.getByText("This page hit an unexpected error")).toBeVisible();
    expect(screen.getByText("state of the union is broken")).toBeVisible();
    expect(screen.getByRole("button", { name: "Reload" })).toBeVisible();
  });

  it("renders the page untouched when nothing throws", () => {
    render(
      <ErrorBoundary resetKey="/a">
        <p>page content</p>
      </ErrorBoundary>,
    );

    expect(screen.getByText("page content")).toBeVisible();
    expect(screen.queryByText("This page hit an unexpected error")).toBeNull();
  });

  it("clears the error when the route changes", () => {
    render(<Harness />);
    expect(screen.getByText("This page hit an unexpected error")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "stop throwing" }));
    // Still showing the fallback: the same route is still broken.
    expect(screen.getByText("This page hit an unexpected error")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "navigate" }));

    expect(screen.getByText("page content")).toBeVisible();
    expect(screen.queryByText("This page hit an unexpected error")).toBeNull();
  });

  it("does not remount the page when the reset key is unchanged", () => {
    // A `key` here would tear the page down on every shallow URL write; pages
    // that keep their filters in the query string would lose their state.
    function Counter() {
      const [count, setCount] = useState(0);
      return (
        <button type="button" onClick={() => setCount((n) => n + 1)}>
          count {count}
        </button>
      );
    }
    const { rerender } = render(
      <ErrorBoundary resetKey="/secrets">
        <Counter />
      </ErrorBoundary>,
    );

    fireEvent.click(screen.getByRole("button", { name: "count 0" }));
    rerender(
      <ErrorBoundary resetKey="/secrets">
        <Counter />
      </ErrorBoundary>,
    );

    expect(screen.getByRole("button", { name: "count 1" })).toBeVisible();
  });
});
