import { useEffect, useState } from "react";

/**
 * A clock that ticks every `intervalMs` (default 30 s) while the tab is
 * visible, so relative timestamps ("2m ago") stay honest without a reload.
 * Pass the value to `formatRelative(ms, now)`; using it as a dependency is
 * what makes the cell re-render. A non-positive interval never ticks.
 */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (intervalMs <= 0) return;
    const tick = () => setNow(Date.now());
    const id = window.setInterval(tick, intervalMs);
    // A backgrounded tab throttles timers; catch up the moment it returns.
    const onVisibility = () => {
      if (!document.hidden) tick();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearInterval(id);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [intervalMs]);
  return now;
}
