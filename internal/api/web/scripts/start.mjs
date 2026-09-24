import { access } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { loadServerEnv, readServerConfig } from "../server/config.mjs";
import { createPanelServer } from "../server/http.mjs";

const directory = fileURLToPath(new URL("../", import.meta.url));
const config = readServerConfig(loadServerEnv(directory));
const distDirectory = fileURLToPath(new URL("../dist/", import.meta.url));
await access(new URL("../dist/index.html", import.meta.url)).catch(() => {
  throw new Error("Panel build missing. Run npm run build first.");
});
const server = createPanelServer({ ...config, distDirectory });
server.once("error", (error) => {
  console.error(`Panel failed to listen: ${error.code || error.message}`);
  process.exitCode = 1;
});
server.listen(config.port, config.host, () =>
  console.log(
    `Prism panel: http://${config.host.includes(":") ? `[${config.host}]` : config.host}:${config.port}/ui/`,
  ),
);
for (const signal of ["SIGINT", "SIGTERM"])
  process.once(signal, () => {
    server.close();
    server.closeAllConnections();
  });
