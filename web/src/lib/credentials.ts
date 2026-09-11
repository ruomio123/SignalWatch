// A stable login identity is separate from the short-lived access token.
// Renewal changes credentials without remounting pages or discarding forms.
type Credentials = { id: string; accessToken: string };
const storageKey = "signalwatch.session";
const legacyKey = "signalwatch.access_token";
function read(): Credentials | null {
  try {
    const raw = localStorage.getItem(storageKey);
    if (raw) {
      const value = JSON.parse(raw);
      if (
        typeof value.id === "string" &&
        typeof value.accessToken === "string" &&
        value.id &&
        value.accessToken
      )
        return value;
    }
    const legacy = localStorage.getItem(legacyKey);
    return legacy ? { id: legacy, accessToken: legacy } : null;
  } catch {
    return null;
  }
}
let credentials = read();
let generation = 0;
let ending = false;
const listeners = new Set<() => void>();
function notify() {
  listeners.forEach((listener) => listener());
}
function persist() {
  try {
    if (credentials)
      localStorage.setItem(storageKey, JSON.stringify(credentials));
    else localStorage.removeItem(storageKey);
    localStorage.removeItem(legacyKey);
  } catch {
    /* A login remains usable in this tab when storage is unavailable. */
  }
}
export const loginIdentity = () => credentials?.id ?? "";
export const credentialGeneration = () => generation;
export const isEndingSession = () => ending;
export function accessFor(identity: string) {
  return credentials?.id === identity ? credentials.accessToken : "";
}
export function subscribeCredentials(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
export function beginSession(accessToken: string) {
  generation++;
  ending = false;
  credentials = {
    id: [...crypto.getRandomValues(new Uint8Array(16))]
      .map((n) => n.toString(16).padStart(2, "0"))
      .join(""),
    accessToken,
  };
  persist();
  notify();
}
// This is only an identity-change guard, never token validation/authorization.
// It prevents a shared browser cookie for a different account from replaying
// an old account's form submission after renewal.
function subject(raw: string): string | undefined {
  try {
    const part = raw.split(".")[1];
    const payload = JSON.parse(
      atob(part.replace(/-/g, "+").replace(/_/g, "/")),
    );
    return typeof payload.sub === "string" ? payload.sub : undefined;
  } catch {
    return undefined;
  }
}
export function renewCredentials(
  identity: string,
  expectedGeneration: number,
  accessToken: string,
) {
  if (
    generation !== expectedGeneration ||
    ending ||
    !credentials ||
    credentials.id !== identity
  )
    throw new DOMException("Session changed", "AbortError");
  const previousUser = subject(credentials.accessToken),
    nextUser = subject(accessToken);
  if (previousUser && nextUser && previousUser !== nextUser) {
    clearCredentials(expectedGeneration);
    throw new DOMException("Account changed; sign in again", "AbortError");
  }
  credentials = { ...credentials, accessToken };
  persist();
}
export function clearCredentials(expectedGeneration = generation) {
  if (expectedGeneration !== generation) return;
  generation++;
  ending = false;
  credentials = null;
  persist();
  notify();
}
export function beginLogout() {
  ending = true;
  return ++generation;
}
export function cancelLogout(expectedGeneration: number) {
  if (generation === expectedGeneration) ending = false;
}
window.addEventListener("storage", (event) => {
  if (event.key !== storageKey && event.key !== legacyKey && event.key !== null)
    return;
  const next = read();
  if (next?.id === credentials?.id) {
    credentials = next;
    return;
  }
  credentials = next;
  generation++;
  ending = false;
  notify();
});
