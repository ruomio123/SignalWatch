import {
  accessFor,
  credentialGeneration,
  renewCredentials,
  clearCredentials,
  isEndingSession,
  beginLogout,
  cancelLogout,
} from "./credentials";
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    public requestId: string,
    message: string,
    public retryAfterSeconds?: number,
    public callId?: string,
    public resetAt?: string,
  ) {
    super(message);
  }
}
export interface RequestOptions {
  method?: string;
  body?: unknown;
  token?: string;
  signal?: AbortSignal;
  etag?: string;
}
function abortedSession() {
  return new DOMException("Session changed", "AbortError");
}
let renewal: { generation: number; promise: Promise<string> } | undefined;
async function responseData(response: Response): Promise<unknown> {
  if (response.status === 204) return undefined;
  const data = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new ApiError(
      response.status,
      data.code ?? "UNKNOWN",
      data.request_id ?? "",
      data.message ?? "请求失败",
      data.retry_after_seconds,
      data.call_id,
      data.reset_at,
    );
  return data;
}
function refreshAccess(identity: string, generation: number): Promise<string> {
  if (credentialGeneration() !== generation || isEndingSession())
    return Promise.reject(abortedSession());
  if (renewal?.generation === generation) return renewal.promise;
  const promise = (async () => {
    try {
      const response = await fetch("/api/v2/auth/refresh", {
        method: "POST",
        credentials: "same-origin",
        headers: { "X-SignalWatch-Session": "1" },
        signal: AbortSignal.timeout(10000),
      });
      const data = (await responseData(response)) as { access_token?: string };
      if (!data || typeof data.access_token !== "string" || !data.access_token)
        throw new ApiError(
          503,
          "AUTH_UNAVAILABLE",
          "",
          "登录续期暂时不可用，请重试。",
        );
      renewCredentials(identity, generation, data.access_token);
      return data.access_token;
    } catch (error) {
      // Transport failures and server faults are recoverable; keep the session.
      if (error instanceof ApiError && error.status === 401)
        clearCredentials(generation);
      throw error;
    }
  })();
  renewal = { generation, promise };
  void promise
    .finally(() => {
      if (renewal?.promise === promise) renewal = undefined;
    })
    .catch(() => {});
  return promise;
}
export async function request<T>(
  path: string,
  { method = "GET", body, token, signal, etag }: RequestOptions = {},
): Promise<T> {
  const generation = credentialGeneration();
  const serialized = body === undefined ? undefined : JSON.stringify(body);
  let access = token ? accessFor(token) : "";
  if (token && (!access || isEndingSession())) throw abortedSession();
  const send = (access: string) =>
    fetch("/api/v2" + path, {
      method,
      signal,
      credentials: "same-origin",
      headers: {
        ...(serialized !== undefined
          ? { "Content-Type": "application/json" }
          : {}),
        ...(access ? { Authorization: `Bearer ${access}` } : {}),
        ...(etag ? { "If-Match": etag } : {}),
      },
      body: serialized,
    });
  let response = await send(access);
  signal?.throwIfAborted();
  if (token && credentialGeneration() !== generation) throw abortedSession();
  if (response.status === 401 && token) {
    // Another request may already have renewed this token. Reuse it rather
    // than starting a second refresh for the same batch of stale responses.
    const current = accessFor(token);
    access =
      current && current !== access
        ? current
        : await refreshAccess(token, generation);
    signal?.throwIfAborted();
    if (credentialGeneration() !== generation || isEndingSession())
      throw abortedSession();
    response = await send(access);
    signal?.throwIfAborted();
    if (credentialGeneration() !== generation) throw abortedSession();
    if (response.status === 401) clearCredentials(generation);
  }
  const data = await responseData(response);
  signal?.throwIfAborted();
  if (token && credentialGeneration() !== generation) throw abortedSession();
  return data as T;
}
export async function logoutSession() {
  const generation = beginLogout();
  try {
    const response = await fetch("/api/v2/auth/logout", {
      method: "POST",
      credentials: "same-origin",
      headers: { "X-SignalWatch-Session": "1" },
      signal: AbortSignal.timeout(10000),
    });
    await responseData(response);
    clearCredentials(generation);
  } catch (error) {
    cancelLogout(generation);
    throw error;
  }
}
const messages: Record<string, string> = {
  SUBSCRIPTION_VERSION_CONFLICT: "订阅已被修改，请重新载入后重试。",
  AI_CONFIGURATION_VERSION_CONFLICT: "AI 配置已变化，请重新载入后重试。",
  AUTH_INVALID_CREDENTIALS: "邮箱或密码不正确。",
  SUBSCRIPTION_LIMIT_REACHED: "最多启用 20 个订阅。",
  AI_CONFIGURATION_REQUIRED: "请先配置 AI 供应商。",
  AI_CONFIGURATION_INVALID: "供应商拒绝了 API Key，请检查配置。",
  AI_MODEL_UNAVAILABLE: "当前模型已不可用，请切换模型。",
  AI_TASK_CONFLICT: "任务状态已变化，请重新查询。",
  AI_RATE_LIMITED: "调用过于频繁，请等待后再试。",
  AI_MODEL_ACCESS_DENIED: "供应商拒绝访问此模型，请检查模型权限。",
  AI_PROVIDER_RATE_LIMITED: "供应商正在限流，请稍后手动重试。",
  AI_PROVIDER_TIMEOUT: "模型在 30 秒内未完成响应，调用结果未知。",
  AI_PROVIDER_UNAVAILABLE: "模型服务暂时不可用，请稍后手动重试。",
  AI_PROVIDER_REJECTED: "供应商拒绝了请求，请根据诊断编号排查参数或账户状态。",
  AI_RESULT_UNKNOWN: "调用结果未知，系统不会自动再次调用。",
  AI_NETWORK_FAILED: "连接供应商失败，调用结果可能未知。",
  AI_OUTPUT_TRUNCATED: "模型输出达到长度上限，验证未通过。",
  AI_INVALID_OUTPUT: "模型返回内容不符合所需格式。",
  AI_UNAVAILABLE: "AI 内部服务暂时不可用，请根据诊断编号排查。",
  AI_DISABLED: "AI 功能暂未开放。",
  AI_DAILY_LIMIT_REACHED: "今日调用已达上限。",
  AI_CALL_IN_PROGRESS: "另一个 AI 调用正在进行，请稍后重试。",
};
export function errorMessage(error: unknown): string {
  if (error instanceof ApiError)
    return (
      messages[error.code] ??
      (error.status >= 500 ? "服务暂时不可用，请稍后重试。" : error.message)
    );
  return error instanceof Error ? error.message : "请求失败，请重试。";
}
export function configETag(c: { generation?: string; version?: number }) {
  return c.generation && c.version
    ? `"${c.generation}:${c.version}"`
    : undefined;
}

const outputFailures: Record<string, string> = {
  output_language_mismatch: "模型未按要求用中文撰写报告，本轮未发布。",
  output_invalid_json: "模型返回的内容不是有效的 JSON。",
  output_schema_mismatch: "模型输出的字段、类型或结构不符合约定。",
  output_limit_exceeded: "模型输出的条目数量或文字长度超出上限。",
  evidence_id_unknown: "模型引用了本次材料中不存在的证据片段。",
  evidence_quote_mismatch: "证据引文无法在本次提供的论文原文中精确定位。",
  evidence_quote_length: "证据引文的长度不符合要求。",
  review_incomplete: "结论校验未完整覆盖全部论断。",
};
export function outputFailureMessage(code: string): string | undefined {
  return outputFailures[code];
}

export function aiFailureMessage(code: string): string {
  const output = outputFailureMessage(code);
  if (output) return output;
  const codes: Record<string, string> = {
    rate_limited: "AI_RATE_LIMITED",
    daily_limit: "AI_DAILY_LIMIT_REACHED",
    budget_exhausted: "AI_DAILY_LIMIT_REACHED",
    call_in_progress: "AI_CALL_IN_PROGRESS",
    credential_rejected: "AI_CONFIGURATION_INVALID",
    model_access_denied: "AI_MODEL_ACCESS_DENIED",
    provider_rate_limited: "AI_PROVIDER_RATE_LIMITED",
    timeout: "AI_PROVIDER_TIMEOUT",
    transport_failed: "AI_NETWORK_FAILED",
    provider_unavailable: "AI_PROVIDER_UNAVAILABLE",
    provider_rejected: "AI_PROVIDER_REJECTED",
    output_truncated: "AI_OUTPUT_TRUNCATED",
    invalid_response: "AI_INVALID_OUTPUT",
    invalid_output: "AI_INVALID_OUTPUT",
    storage_failed: "AI_UNAVAILABLE",
  };
  if (code === "result_unknown" || code === "lease_expired")
    return "调用结果未知，系统不会自动再次调用。";
  return messages[codes[code]] ?? "本次生成未完成，请手动重试或查询诊断记录。";
}
