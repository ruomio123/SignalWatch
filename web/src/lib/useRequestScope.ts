import { useCallback, useLayoutEffect, useRef } from "react";
// Event handlers acquire the current signal at invocation. StrictMode may
// replay effects, so a cancelled mount's controller is never reused.
export function useRequestScope(context?: string) {
  const controller = useRef(new AbortController());
  useLayoutEffect(() => {
    if (controller.current.signal.aborted)
      controller.current = new AbortController();
    const active = controller.current;
    return () => active.abort();
  }, [context]);
  // Capture this signal once before awaiting; a later call belongs to the new view.
  return useCallback(() => controller.current.signal, []);
}
