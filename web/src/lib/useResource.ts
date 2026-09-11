import { useCallback, useEffect, useState } from "react";
// Each load owns its cancellation scope. An old response can never commit into
// a new route, filter or account, even when a transport ignores cancellation.
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [revision, setRevision] = useState(0);
  const [state, setState] = useState<{
    data?: T;
    error?: unknown;
    loading: boolean;
  }>({ loading: true });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setState({ loading: true });
    load(controller.signal).then(
      (data) => {
        if (current) setState({ data, loading: false });
      },
      (error) => {
        if (current) setState({ error, loading: false });
      },
    );
    return () => {
      current = false;
      controller.abort();
    };
  }, [load, revision]);
  const reload = useCallback(() => setRevision((n) => n + 1), []);
  return { ...state, reload };
}
