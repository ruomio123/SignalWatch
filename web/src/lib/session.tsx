import {
  createContext,
  useContext,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import {
  beginSession,
  loginIdentity,
  subscribeCredentials,
} from "./credentials";
import { logoutSession } from "./api";
const Session = createContext<{
  token: string;
  login: (token: string) => void;
  logout: () => void;
  logoutError?: unknown;
  loggingOut: boolean;
}>({ token: "", login: () => {}, logout: () => {}, loggingOut: false });
export function SessionProvider({ children }: { children: ReactNode }) {
  const token = useSyncExternalStore(subscribeCredentials, loginIdentity);
  const [logoutError, setLogoutError] = useState<unknown>();
  const [loggingOut, setLoggingOut] = useState(false);
  function login(accessToken: string) {
    setLogoutError(undefined);
    beginSession(accessToken);
  }
  async function logout() {
    if (loggingOut) return;
    setLogoutError(undefined);
    setLoggingOut(true);
    try {
      await logoutSession();
    } catch (error) {
      setLogoutError(error);
    } finally {
      setLoggingOut(false);
    }
  }
  return (
    <Session.Provider value={{ token, login, logout, logoutError, loggingOut }}>
      {children}
    </Session.Provider>
  );
}
export const useSession = () => useContext(Session);
