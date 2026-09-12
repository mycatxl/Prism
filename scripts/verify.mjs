import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { once } from "node:events";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const tags = "with_quic with_wireguard with_grpc with_utls with_gvisor http2legacy";
const tasks = {
  wireguard: ["go", "test", "-p", "2", "-tags", tags, "-timeout", "90s", "-count=5", "-run", "^TestSingboxBuilder_ParseExtendedProtocols/wireguard$", "./internal/outbound"],
  backend: ["go", "test", "-p", "2", "-tags", tags, "-timeout", "3m", "./cmd/...", "./internal/..."],
  race: ["go", "test", "-race", "-p", "2", "-tags", tags, "-timeout", "3m", "./cmd/...", "./internal/..."],
  build: ["make", "build"],
  binary: ["make", "backend"],
  candidate: ["go", "build", "-buildvcs=false", "-tags", tags, "-o", join(root, ".local", "verification", "prism-candidate"), "./cmd/prism"],
  "browser-candidate": ["env", "PRISM_TEST_BACKEND=" + join(root, ".local", "verification", "prism-candidate"), "npm", "--prefix", "web", "run", "test:e2e"],
  "browser-live": ["env", "PRISM_TEST_LIVE_QUALITY=1", "PRISM_TEST_BACKEND=" + join(root, ".local", "verification", "prism-candidate"), "npm", "--prefix", "web", "run", "test:e2e"],
  lint: ["npm", "--prefix", "web", "run", "lint"],
  config: ["npm", "--prefix", "web", "run", "test:config"],
  "npm-audit": ["npm", "--prefix", "web", "audit", "--json"],
  browser: ["npm", "--prefix", "web", "run", "test:e2e"],
  vet: ["go", "vet", "-tags", tags, "./cmd/...", "./internal/..."],
  vulnerabilities: [process.env.PRISM_GOVULNCHECK || "/tmp/prism-audit-bin/govulncheck", "-json", "-tags", tags.replaceAll(" ", ","), "./cmd/...", "./internal/..."],
};
const name = process.argv[2];
const command = tasks[name];
if (!command) throw new Error(`Choose a check: ${Object.keys(tasks).join(", ")}`);
const directory = join(root, ".local", "verification");
await mkdir(directory, { recursive: true, mode: 0o700 });
const logPath = join(directory, `${name}.log`);
const statusPath = join(directory, `${name}.json`);
const startedAt = new Date().toISOString();
await writeFile(statusPath, JSON.stringify({ name, startedAt, status: "running" }, null, 2), { mode: 0o600 });
const log = createWriteStream(logPath, { mode: 0o600 });
const errorLog = name === "vulnerabilities"
  ? createWriteStream(join(directory, `${name}.stderr.log`), { mode: 0o600 })
  : log;
const child = spawn(command[0], command.slice(1), {
  cwd: root,
  env: { ...process.env, GOCACHE: process.env.GOCACHE || "/tmp/prismx-review-go-cache" },
  stdio: ["ignore", "pipe", "pipe"],
});
child.stdout.pipe(log, { end: false });
child.stderr.pipe(errorLog, { end: false });
let exitCode, signal, error;
try {
  [exitCode, signal] = await once(child, "close");
} catch (failure) {
  exitCode = 1;
  error = failure.message;
  log.write(error + "\n");
}
log.end();
await once(log, "finish");
if (errorLog !== log) {
  errorLog.end();
  await once(errorLog, "finish");
}
const status = exitCode !== 0 ? "failed" : name === "vulnerabilities" ? "completed" : "passed";
const result = { name, startedAt, finishedAt: new Date().toISOString(), status, exitCode, signal, error, logPath };
await writeFile(statusPath, JSON.stringify(result, null, 2) + "\n", { mode: 0o600 });
console.log(JSON.stringify(result));
process.exitCode = exitCode ?? 1;
