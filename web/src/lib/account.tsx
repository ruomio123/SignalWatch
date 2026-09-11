import { createContext, useCallback, useContext, type ReactNode } from "react";
import { request } from "./api";
import { useSession } from "./session";
import { useResource } from "./useResource";
import type { Profile } from "./types";
type Account = ReturnType<typeof useResource<Profile>>;
const Context = createContext<Account | null>(null);
export function AccountProvider({ children }: { children: ReactNode }) {
  const { token } = useSession();
  const account = useResource(
    useCallback(
      (signal: AbortSignal) => request<Profile>("/me", { token, signal }),
      [token],
    ),
  );
  return <Context.Provider value={account}>{children}</Context.Provider>;
}
export function useAccount() {
  const account = useContext(Context);
  if (!account) throw new Error("AccountProvider is required");
  return account;
}
