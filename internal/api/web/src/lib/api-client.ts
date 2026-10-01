import { getStoredAuthToken } from "../features/auth/auth-store";

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
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: JsonValue;
  auth?: boolean;
  token?: string;
  signal?: AbortSignal;
};

function buildURL(path: string): string {
  if (path.startsWith("http://") || path.startsWith("https://")) {
    return path;
  }
  return `${API_BASE_URL}${path}`;
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

/**
 * Whether this deployment needs an admin token, asked before one exists.
 *
 * The login page used to probe `GET /api/v1/system/info` without credentials and read
 * 401 as "auth is on". The answer was right and the request was wrong: Chromium logs
 * the 401 response itself, so the first screen of a secured deployment could never be
 * console-clean, and the question was expressed as a failure. The SPA handler answers
 * it directly at /ui/session.json.
 *
 * Fails closed: an unreadable answer means "a token is required", which is the safe
 * default to show a stranger.
 */
export async function fetchAuthMode(signal?: AbortSignal): Promise<boolean> {
  const response = await fetch(buildURL("/ui/session.json"), {
    headers: { Accept: "application/json" },
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(10_000)]) : AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new ApiError(response.status, "SESSION_MODE_UNAVAILABLE", "无法确定登录方式", null);
  }
  const body = (await response.json()) as { auth_required?: boolean };
  return body.auth_required !== false;
}

export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, auth = true, token, signal } = options;
  if (method !== "GET" && !navigator.onLine) {
    throw new ApiError(0, "OFFLINE", "离线时无法提交操作", null);
  }
  const headers = new Headers();

  if (body !== undefined) {
    headers.set("Content-Type", "application/json; charset=utf-8");
  }

  if (auth) {
    const resolvedToken = token?.trim() || getStoredAuthToken();
    if (resolvedToken) {
      headers.set("Authorization", `Bearer ${resolvedToken}`);
    }
  }

  const response = await fetch(buildURL(path), {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(30_000)]) : AbortSignal.timeout(30_000),
  });

  if (!response.ok) {
    const parsed = await parseErrorBody(response);
    const code = parsed?.error?.code ?? "HTTP_ERROR";
    const message = parsed?.error?.message ?? response.statusText;
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
