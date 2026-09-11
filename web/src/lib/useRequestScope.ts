import { useEffect, useRef } from "react";
// Event handlers acquire the current signal at invocation. StrictMode may
// replay effects, so a cancelled mount's controller is never reused.
export function useRequestScope() {
  const controller = useRef(new AbortController());
  useEffect(() => {
    if (controller.current.signal.aborted)
      controller.current = new AbortController();
    return () => controller.current.abort();
  }, []);
  return () => controller.current.signal;
}
