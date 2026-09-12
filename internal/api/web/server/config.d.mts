export type ServerConfig = { host: string; port: number; apiTarget: string };
export function loadServerEnv(
  directory: string,
  overrides?: Record<string, string | undefined>,
  mode?: string,
): Record<string, string | undefined>;
export function readServerConfig(
  env: Record<string, string | undefined>,
): ServerConfig;
