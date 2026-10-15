import { clearSession, setSession, type SessionInfo } from "../features/auth/auth-store";

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL?.trim() ?? "";

type Primitive = string | number | boolean | null;
type JsonValue = Primitive | JsonValue[] | { [key: string]: JsonValue };

export type ApiErrorBody = {
  error?: {
    code?: string;
    message?: string;
  };
};

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: ApiErrorBody | null;

  constructor(status: number, code: string, message: string, body: ApiErrorBody | null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: JsonValue;
  signal?: AbortSignal;
};

function buildURL(path: string): string {
  if (path.startsWith("http://") || path.startsWith("https://")) {
    return path;
  }
  return `${API_BASE_URL}${path}`;
}

function timeoutSignal(signal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

function csrfToken(): string {
  if (typeof document === "undefined") {
    return "";
  }
  const cookie = document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith("prism_csrf="));
  if (!cookie) {
    return "";
  }
  try {
    return decodeURIComponent(cookie.slice("prism_csrf=".length));
  } catch {
    return "";
  }
}

function isSafeMethod(method: string): boolean {
  return method === "GET" || method === "HEAD" || method === "OPTIONS";
}

async function parseErrorBody(response: Response): Promise<ApiErrorBody | null> {
  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) {
    return null;
  }
  try {
    return (await response.json()) as ApiErrorBody;
  } catch {
    return null;
  }
}
export async function fetchSession(signal?: AbortSignal): Promise<SessionInfo> {
  const response = await fetch(buildURL("/ui/session.json"), {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
    signal: timeoutSignal(signal, 10_000),
  });
  if (!response.ok) {
    throw new ApiError(response.status, "SESSION_MODE_UNAVAILABLE", "无法确定登录方式", null);
  }
  const body = (await response.json()) as SessionInfo;
  const session: SessionInfo = {
    auth_required: body.auth_required !== false,
    authenticated: body.authenticated === true,
    user: body.user,
    must_enrol_totp: body.must_enrol_totp === true,
  };
  setSession(session);
  return session;
}

/** Whether this deployment requires a logged-in console session. */
export async function fetchAuthMode(signal?: AbortSignal): Promise<boolean> {
  const session = await fetchSession(signal);
  return session.auth_required;
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, signal } = options;
  if (method !== "GET" && typeof navigator !== "undefined" && !navigator.onLine) {
    throw new ApiError(0, "OFFLINE", "离线时无法提交操作", null);
  }

  const headers = new Headers();
  headers.set("Accept", "application/json");
  if (body !== undefined) {
    headers.set("Content-Type", "application/json; charset=utf-8");
  }
  if (!isSafeMethod(method)) {
    const token = csrfToken();
    if (token) {
      headers.set("X-Prism-CSRF", token);
    }
  }

  const response = await fetch(buildURL(path), {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
    signal: timeoutSignal(signal, 30_000),
  });

  if (!response.ok) {
    const parsed = await parseErrorBody(response);
    const code = parsed?.error?.code ?? "HTTP_ERROR";
    const message = parsed?.error?.message ?? response.statusText;
    if (response.status === 401) {
      clearSession();
    }
    throw new ApiError(response.status, code, message, parsed);
  }

  if (response.status === 204) {
    return undefined as T;
  }

  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) {
    throw new ApiError(response.status, "INVALID_RESPONSE", "服务返回了无法识别的数据", null);
  }

  return (await response.json()) as T;
}

export type LoginResponse = {
  authenticated: true;
  user: NonNullable<SessionInfo["user"]>;
  expires_at_ns: number;
};

export async function login(username: string, password: string, totp: string): Promise<LoginResponse> {
  const response = await apiRequest<LoginResponse>("/api/v1/auth/login", {
    method: "POST",
    body: { username, password, totp },
  });
  setSession({
    auth_required: true,
    authenticated: true,
    user: response.user,
    must_enrol_totp: false,
  });
  return response;
}

export async function logout(): Promise<void> {
  await apiRequest<void>("/api/v1/auth/logout", { method: "POST" });
  clearSession();
}
