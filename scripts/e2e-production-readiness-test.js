#!/usr/bin/env node
"use strict";

const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");

const ROOT = path.resolve(__dirname, "..");
const IMAGE = process.env.AEGIS_READINESS_IMAGE || "aegiscrawler:readiness";
const SKIP_BUILD = process.env.AEGIS_READINESS_SKIP_BUILD === "1";
const ADMIN_KEY = "stage11-admin-fixture-key";
const WORKER_KEY = "stage11-worker-fixture-key";
const METRICS_KEY = "stage11-metrics-fixture-key";
const SWAGGER_KEY = "stage11-swagger-fixture-key";
const ENCRYPTION_KEY = "stage11-encryption-fixture-key-32-bytes";
const TOKEN_NAME = "Stage 11 recovery token";
const ENFORCED_LLM_ENV = {
  LLM_ENABLED: "true",
  LLM_POLICY_MODE: "enforced",
  LLM_PRIMARY_PROVIDER: "stage11-synthetic",
  LLM_PRIMARY_ADAPTER: "openai",
  LLM_PRIMARY_MODEL: "stage11-synthetic-model",
  LLM_PRIMARY_BASE_URL: "https://provider.example.test/v1",
  LLM_PRIMARY_API_KEY: "stage11-synthetic-provider-key",
  LLM_PRIMARY_REQUEST_TIMEOUT: "10s",
  LLM_PRIMARY_TEMPERATURE: "0",
  LLM_PRIMARY_STRICT_TOOL_OUTPUT: "false",
  LLM_PRIMARY_ENABLE_THINKING: "false",
  LLM_PRIMARY_INPUT_USD_PER_MILLION: "1",
  LLM_PRIMARY_OUTPUT_USD_PER_MILLION: "1",
  LLM_PRIMARY_MAX_INPUT_TOKENS: "1000",
  LLM_PRIMARY_MAX_OUTPUT_TOKENS: "100",
  LLM_PRIMARY_OUTPUT_CAP_DIALECT: "max_tokens",
  LLM_PRIMARY_PRICE_REVISION: "stage11-synthetic-v1",
  LLM_GLOBAL_MAX_REQUEST_USD: "0.001100001",
  LLM_GLOBAL_DAILY_BUDGET_USD: "0.01",
  LLM_WORKSPACE_BUDGETS_JSON: JSON.stringify({
    default: { maxRequestUSD: "0.001100001", dailyBudgetUSD: "0.01" },
  }),
  LLM_REQUIREMENT_MAX_ATTEMPTS: "2",
  LLM_DSL_GENERATION_MAX_ATTEMPTS: "3",
  LLM_DSL_MAX_REPAIRS: "1",
  LLM_SELECTOR_MAX_REPAIRS: "1",
};
const LEGACY_ROLLBACK_LLM_ENV = {
  LLM_ENABLED: "true",
  LLM_POLICY_MODE: "legacy",
  LLM_PROVIDER: "openai",
  LLM_API_KEY: "stage11-synthetic-legacy-key",
  LLM_BASE_URL: "https://provider.example.test/v1",
  LLM_MODEL: "stage11-synthetic-legacy-model",
  LLM_MAX_RETRIES: "0",
};
const DISABLED_ROLLBACK_LLM_ENV = {
  LLM_ENABLED: "false",
  LLM_POLICY_MODE: "legacy",
};

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function runResult(command, args, options = {}) {
  return spawnSync(command, args, {
    cwd: ROOT,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
    ...options,
  });
}

function run(command, args, label, options = {}) {
  const result = runResult(command, args, options);
  if (result.error || result.status !== 0) {
    const detail = [result.error?.message, result.stderr, result.stdout]
      .filter(Boolean)
      .join("\n")
      .trim();
    throw new Error(`${label} failed${detail ? `: ${detail}` : ""}`);
  }
  return (result.stdout || "").trim();
}

function docker(args, label, options = {}) {
  return run("docker", args, label, options);
}

function getFreePort() {
  return new Promise((resolve, reject) => {
    const server = http.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = address && typeof address === "object" ? address.port : 0;
      server.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}

async function request(baseUrl, route, options = {}, timeoutMs = 5000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(`${baseUrl}${route}`, {
      ...options,
      signal: controller.signal,
    });
    const text = await response.text();
    return { response, text };
  } finally {
    clearTimeout(timer);
  }
}

async function waitForReady(baseUrl, containerName, timeoutMs = 45000) {
  const deadline = Date.now() + timeoutMs;
  let lastError = "not started";
  while (Date.now() < deadline) {
    try {
      const { response, text } = await request(baseUrl, "/health?ready=1", {}, 2000);
      if (response.ok && text.includes('"status":"ok"')) return;
      lastError = `HTTP ${response.status}: ${text}`;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    const running = runResult("docker", ["inspect", "--format", "{{.State.Running}}", containerName]);
    if (running.status === 0 && running.stdout.trim() === "false") {
      throw new Error(`${containerName} exited before readiness: ${docker(["logs", containerName], "read failed container logs")}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`${containerName} did not become ready: ${lastError}`);
}

async function waitForContainerHealth(containerName, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let status = "starting";
  while (Date.now() < deadline) {
    status = docker(
      ["inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}", containerName],
      "inspect container health",
    );
    if (status === "healthy") return;
    if (status === "unhealthy" || status === "missing") break;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`${containerName} health check ended as ${status}`);
}

function makeWritableTree(root) {
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const fullPath = path.join(root, entry.name);
    if (entry.isDirectory()) {
      makeWritableTree(fullPath);
      fs.chmodSync(fullPath, 0o777);
    } else {
      fs.chmodSync(fullPath, 0o666);
    }
  }
  fs.chmodSync(root, 0o777);
}

function containerArgs(name, dataDir, port, llmEnvironment = DISABLED_ROLLBACK_LLM_ENV) {
  const args = [
    "run",
    "--detach",
    "--name",
    name,
    "--init",
    "--read-only",
    "--tmpfs",
    "/tmp:rw,noexec,nosuid,size=64m",
    "--cap-drop=ALL",
    "--security-opt=no-new-privileges:true",
    "--stop-timeout=12",
    "--publish",
    `127.0.0.1:${port}:8080`,
    "--mount",
    `type=bind,src=${dataDir},dst=/data`,
    "--env",
    "LISTEN_ADDR=:8080",
    "--env",
    "DATABASE_PATH=/data/opencrawler.db",
    "--env",
    "SQLITE_JOURNAL_MODE=WAL",
    "--env",
    "REQUIRE_SECURITY_KEYS=true",
    "--env",
    `ADMIN_API_KEY=${ADMIN_KEY}`,
    "--env",
    `WORKER_API_KEY=${WORKER_KEY}`,
    "--env",
    `VARIABLE_ENCRYPTION_KEY=${ENCRYPTION_KEY}`,
    "--env",
    `METRICS_API_KEY=${METRICS_KEY}`,
    "--env",
    `SWAGGER_API_KEY=${SWAGGER_KEY}`,
    "--env",
    "FEATURE_RECORDING_V2=true",
    "--env",
    "FEATURE_WORKFLOW_V2=true",
    "--env",
    "FEATURE_WORKER_PROTOCOL_V2=true",
    "--env",
    "FEATURE_MCP=true",
    "--env",
    "LOG_LEVEL=warn",
  ];
  for (const [key, value] of Object.entries(llmEnvironment)) {
    args.push("--env", `${key}=${value}`);
  }
  args.push(IMAGE);
  return args;
}

function removeContainer(name) {
  runResult("docker", ["rm", "--force", name]);
}

async function verifyImageRuntime(baseUrl, containerName) {
  const inspect = JSON.parse(docker(["inspect", containerName], "inspect readiness container"))[0];
  assert(inspect.Config.User === "appuser", `image runs as unexpected user ${inspect.Config.User}`);
  assert(inspect.HostConfig.ReadonlyRootfs === true, "root filesystem is not read-only");
  assert((inspect.HostConfig.CapDrop || []).includes("ALL"), "container did not drop Linux capabilities");
  assert((inspect.HostConfig.SecurityOpt || []).includes("no-new-privileges:true"), "no-new-privileges is not enabled");

  const uid = docker(["exec", containerName, "id", "-u"], "inspect runtime user");
  assert(uid === "1000", `container process uses UID ${uid}, expected 1000`);

  const health = await request(baseUrl, "/health?ready=1");
  assert(health.response.status === 200 && health.text.includes('"status":"ok"'), "database readiness probe failed");

  const admin = await request(baseUrl, "/admin/", { headers: { Accept: "text/html" } });
  assert(admin.response.status === 200, `embedded Admin UI returned ${admin.response.status}`);
  assert(admin.text.includes('<div id="root"></div>'), "Admin UI index is not embedded in the image");
  assert(!admin.text.includes("管理后台未构建"), "image served the unbuilt Admin UI placeholder");
  assert(admin.response.headers.get("x-frame-options") === "DENY", "Admin UI omitted clickjacking protection");
  const assetPath = admin.text.match(/<script[^>]+src="([^"]+\.js)"/)?.[1];
  assert(assetPath, "Admin UI index did not reference a JavaScript asset");
  const assetUrl = new URL(assetPath, baseUrl);
  const assetResponse = await fetch(assetUrl);
  const assetBody = await assetResponse.text();
  assert(assetResponse.ok && assetBody.length > 100, "embedded Admin UI JavaScript asset is unavailable");

  const capabilities = await request(baseUrl, "/api/v1/capabilities");
  assert(capabilities.response.ok, "capability negotiation failed");
  const capabilityJSON = JSON.parse(capabilities.text);
  assert(capabilityJSON.features?.recordingV2 === true, "recording v2 is not enabled");
  assert(capabilityJSON.features?.workflowV2 === true, "workflow v2 is not enabled");
  assert(capabilityJSON.features?.mcp === true, "MCP is not enabled");
  assert(capabilityJSON.workerProtocolVersions?.includes("v2"), "worker protocol v2 is not enabled");

  const unauthenticatedMetrics = await request(baseUrl, "/metrics");
  assert(unauthenticatedMetrics.response.status === 401, "metrics endpoint did not fail closed");
  const metrics = await request(baseUrl, "/metrics", {
    headers: { Authorization: `Bearer ${METRICS_KEY}` },
  });
  assert(metrics.response.ok && metrics.text.includes("opencrawler_db_size_bytes"), "authenticated metrics scrape failed");

  const unauthenticatedSwagger = await request(baseUrl, "/swagger.json");
  assert(unauthenticatedSwagger.response.status === 401, "Swagger endpoint did not fail closed");
}

async function createAndVerifyToken(baseUrl) {
  const created = await request(baseUrl, "/admin/mcp/tokens", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${ADMIN_KEY}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ name: TOKEN_NAME, permissions: ["read", "write"] }),
  });
  assert(created.response.status === 201, `could not create readiness token: ${created.response.status} ${created.text}`);
  const token = JSON.parse(created.text);
  assert(token.id && token.token?.startsWith("aegis_mcp_"), "token creation omitted one-time credentials");
  await verifyToken(baseUrl, token.id, token.token);
  return token.id;
}

async function verifyToken(baseUrl, tokenId, plaintextToken) {
  const fetched = await request(baseUrl, "/admin/mcp/tokens", {
    headers: { Authorization: `Bearer ${ADMIN_KEY}` },
  });
  assert(fetched.response.ok, `persisted token lookup returned ${fetched.response.status}: ${fetched.text}`);
  const tokens = JSON.parse(fetched.text).tokens;
  const token = tokens?.find((item) => item.id === tokenId);
  assert(token?.name === TOKEN_NAME, "workspace-scoped token metadata was not persisted");
  assert(!token.token, "token listing exposed one-time plaintext credentials");
  if (plaintextToken) assert(!fetched.text.includes(plaintextToken), "token listing leaked the plaintext token");
}

async function concurrentReadinessProbe(baseUrl) {
  const results = await Promise.all(
    Array.from({ length: 75 }, () => request(baseUrl, "/health?ready=1", {}, 10000)),
  );
  const failures = results.filter(({ response, text }) => !response.ok || !text.includes('"status":"ok"'));
  assert(failures.length === 0, `${failures.length} of 75 concurrent readiness probes failed`);
}

async function readAdminJSON(baseUrl, route) {
  const result = await request(baseUrl, route, {
    headers: { Authorization: `Bearer ${ADMIN_KEY}` },
  });
  let payload;
  try {
    payload = JSON.parse(result.text);
  } catch {
    payload = undefined;
  }
  return { ...result, payload };
}

function metricValue(metrics, name) {
  const line = metrics.split("\n").find((candidate) => candidate.startsWith(`${name} `));
  assert(line, `metrics scrape omitted ${name}`);
  const value = Number(line.trim().split(/\s+/).at(-1));
  assert(Number.isFinite(value), `metric ${name} did not contain a numeric value`);
  return value;
}

async function verifyEnforcedLLMControlPlane(baseUrl, budgetDay = "") {
  const policy = await readAdminJSON(baseUrl, "/admin/llm/policy");
  assert(policy.response.ok, `enforced LLM policy returned ${policy.response.status}: ${policy.text}`);
  assert(policy.payload?.mode === "enforced" && policy.payload?.enabled === true,
    `production image did not load enforced LLM mode: ${policy.text}`);
  assert(policy.payload?.productionEligible === true
    && policy.payload?.primary?.productionEligible === true,
  `synthetic HTTPS production policy was not eligible: ${policy.text}`);
  assert(policy.payload?.budgetLedgerBound === true && policy.payload?.reconciled === true,
    `production image did not bind and reconcile its ledger: ${policy.text}`);
  assert(policy.payload?.primary?.provider === "stage11-synthetic"
    && policy.payload?.primary?.priceRevision === "stage11-synthetic-v1",
  `production image reported an unexpected route: ${policy.text}`);
  assert(!policy.text.includes(ENFORCED_LLM_ENV.LLM_PRIMARY_API_KEY),
    "LLM policy endpoint exposed its synthetic provider credential");

  const budgetRoute = budgetDay
    ? `/admin/llm/budget?day=${encodeURIComponent(budgetDay)}`
    : "/admin/llm/budget";
  const budget = await readAdminJSON(baseUrl, budgetRoute);
  assert(budget.response.ok, `enforced LLM budget returned ${budget.response.status}: ${budget.text}`);
  assert(budget.payload?.global?.settledUsd === "0"
    && budget.payload?.global?.activeReservedUsd === "0"
    && budget.payload?.global?.remainingUsd === "0.01",
  `production image reported an unexpected ledger snapshot: ${budget.text}`);

  const metrics = await request(baseUrl, "/metrics", {
    headers: { Authorization: `Bearer ${METRICS_KEY}` },
  });
  assert(metrics.response.ok, `enforced metrics scrape returned ${metrics.response.status}: ${metrics.text}`);
  assert(metricValue(metrics.text, "opencrawler_llm_budget_remaining_usd_nanos") === 10000000,
    "startup did not publish the non-zero hard-budget remaining gauge");
  return budget.payload;
}

async function verifyLegacyRollbackIsRejected(baseUrl) {
  const policy = await readAdminJSON(baseUrl, "/admin/llm/policy");
  assert(policy.response.ok, `legacy rollback policy returned ${policy.response.status}: ${policy.text}`);
  assert(policy.payload?.mode === "legacy" && policy.payload?.enabled === true,
    `legacy rollback fixture did not enable its pre-ledger LLM path: ${policy.text}`);
  assert(policy.payload?.productionEligible === false,
    "an LLM-enabled pre-ledger rollback was incorrectly production eligible");
}

async function verifyDisabledRollback(baseUrl) {
  const ready = await request(baseUrl, "/health?ready=1");
  assert(ready.response.ok && ready.text.includes('"status":"ok"'),
    `LLM-disabled rollback was not ready: ${ready.response.status} ${ready.text}`);
  const policy = await readAdminJSON(baseUrl, "/admin/llm/policy");
  assert(policy.response.ok, `disabled rollback policy returned ${policy.response.status}: ${policy.text}`);
  assert(policy.payload?.enabled === false && policy.payload?.productionEligible === false,
    `pre-ledger rollback did not keep LLM traffic disabled: ${policy.text}`);
}

async function main() {
  const stamp = `${process.pid}-${Date.now()}`;
  const firstContainer = `aegis-readiness-${stamp}`;
  const restoredContainer = `aegis-restore-${stamp}`;
  const legacyRollbackContainer = `aegis-legacy-rollback-${stamp}`;
  const disabledRollbackContainer = `aegis-disabled-rollback-${stamp}`;
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), "aegis-stage11-"));
  const dataDir = path.join(tempRoot, "data");
  const backupDir = path.join(tempRoot, "backup");
  const restoreDir = path.join(tempRoot, "restore");
  fs.mkdirSync(dataDir, { mode: 0o777 });
  fs.chmodSync(dataDir, 0o777);
  const containers = [
    firstContainer,
    restoredContainer,
    legacyRollbackContainer,
    disabledRollbackContainer,
  ];

  try {
    console.log(`[stage11] validating production image ${IMAGE}`);
    if (!SKIP_BUILD) {
      run("docker", ["build", "--file", "server/Dockerfile", "--tag", IMAGE, "."], "build production image", {
        stdio: "inherit",
      });
    }

    const imageConfig = JSON.parse(docker(["image", "inspect", IMAGE], "inspect production image"))[0].Config;
    assert(imageConfig.User === "appuser", `image default user is ${imageConfig.User}`);
    assert(imageConfig.Healthcheck?.Test?.join(" ").includes("health?ready=1"), "image has no database readiness health check");

    const composeEnv = {
      ...process.env,
      ADMIN_API_KEY: ADMIN_KEY,
      WORKER_API_KEY: WORKER_KEY,
      VARIABLE_ENCRYPTION_KEY: ENCRYPTION_KEY,
      LLM_ENABLED: "false",
      LLM_POLICY_MODE: "legacy",
    };
    const compose = JSON.parse(
      run("docker", ["compose", "config", "--format", "json"], "validate Docker Compose configuration", { env: composeEnv }),
    );
    const composeService = compose.services?.opencrawler;
    assert(composeService?.read_only === true, "Compose does not enforce a read-only root filesystem");
    assert(composeService.cap_drop?.includes("ALL"), "Compose does not drop Linux capabilities");
    assert(composeService.security_opt?.includes("no-new-privileges:true"), "Compose permits privilege escalation");
    assert(composeService.tmpfs?.some((entry) => entry.startsWith("/tmp:")), "Compose has no writable temporary filesystem");
    assert(composeService.ports?.every((port) => port.host_ip === "127.0.0.1"), "Compose exposes plain HTTP beyond loopback");
    assert(composeService.healthcheck?.test?.join(" ").includes("health?ready=1"), "Compose has no readiness health check");
    assert(String(composeService.environment?.LLM_ENABLED) === "false",
      "rollback-safe Compose rendering did not force LLM_ENABLED=false");
    assert(String(composeService.environment?.LLM_POLICY_MODE) === "legacy",
      "rollback-safe Compose rendering did not select the explicit legacy policy mode");

    const failClosed = runResult("docker", ["run", "--rm", IMAGE]);
    assert(failClosed.status !== 0, "production image started without required security keys");
    console.log("[stage11] image fails closed without security keys");

    const firstPort = await getFreePort();
    const firstBaseUrl = `http://127.0.0.1:${firstPort}`;
    docker(containerArgs(firstContainer, dataDir, firstPort, ENFORCED_LLM_ENV), "start readiness container");
    await waitForReady(firstBaseUrl, firstContainer);
    await waitForContainerHealth(firstContainer);
    await verifyImageRuntime(firstBaseUrl, firstContainer);
    const initialBudget = await verifyEnforcedLLMControlPlane(firstBaseUrl);
    const tokenId = await createAndVerifyToken(firstBaseUrl);
    await concurrentReadinessProbe(firstBaseUrl);
    console.log("[stage11] hardened image, embedded Admin UI, auth, metrics, and concurrent readiness passed");

    docker(["stop", "--time", "12", firstContainer], "stop readiness container");
    const exitCode = docker(["inspect", "--format", "{{.State.ExitCode}}", firstContainer], "inspect graceful shutdown");
    assert(exitCode === "0", `server did not shut down cleanly (exit ${exitCode})`);
    fs.cpSync(dataDir, backupDir, { recursive: true });
    assert(fs.existsSync(path.join(backupDir, "opencrawler.db")), "stopped backup omitted the SQLite database");

    docker(["start", firstContainer], "restart readiness container");
    await waitForReady(firstBaseUrl, firstContainer);
    await verifyToken(firstBaseUrl, tokenId);
    docker(["stop", "--time", "12", firstContainer], "stop restarted container");
    removeContainer(firstContainer);
    console.log("[stage11] graceful restart preserved data");

    fs.cpSync(backupDir, restoreDir, { recursive: true });
    makeWritableTree(restoreDir);
    const restorePort = await getFreePort();
    const restoreBaseUrl = `http://127.0.0.1:${restorePort}`;
    docker(containerArgs(restoredContainer, restoreDir, restorePort, ENFORCED_LLM_ENV), "start restored container");
    await waitForReady(restoreBaseUrl, restoredContainer);
    await verifyToken(restoreBaseUrl, tokenId);
    const restoredBudget = await verifyEnforcedLLMControlPlane(restoreBaseUrl, initialBudget.budgetDay);
    assert(JSON.stringify(restoredBudget) === JSON.stringify(initialBudget),
      "stopped-database restore changed the hard-budget ledger snapshot");
    console.log("[stage11] stopped-database backup and restore preserved application state");

    docker(["stop", "--time", "12", restoredContainer], "stop restored container");
    removeContainer(restoredContainer);

    const legacyRollbackPort = await getFreePort();
    const legacyRollbackBaseUrl = `http://127.0.0.1:${legacyRollbackPort}`;
    docker(
      containerArgs(legacyRollbackContainer, restoreDir, legacyRollbackPort, LEGACY_ROLLBACK_LLM_ENV),
      "start legacy rollback fixture",
    );
    await waitForReady(legacyRollbackBaseUrl, legacyRollbackContainer);
    await verifyLegacyRollbackIsRejected(legacyRollbackBaseUrl);
    docker(["stop", "--time", "12", legacyRollbackContainer], "stop legacy rollback fixture");
    removeContainer(legacyRollbackContainer);

    const disabledRollbackPort = await getFreePort();
    const disabledRollbackBaseUrl = `http://127.0.0.1:${disabledRollbackPort}`;
    docker(
      containerArgs(disabledRollbackContainer, restoreDir, disabledRollbackPort, DISABLED_ROLLBACK_LLM_ENV),
      "start disabled rollback fixture",
    );
    await waitForReady(disabledRollbackBaseUrl, disabledRollbackContainer);
    await verifyToken(disabledRollbackBaseUrl, tokenId);
    await verifyDisabledRollback(disabledRollbackBaseUrl);
    console.log("[stage11] pre-ledger rollback remained ineligible until LLM_ENABLED=false");
    console.log("[stage11] unattended production readiness passed");
  } catch (error) {
    for (const container of containers) {
      const logs = runResult("docker", ["logs", container]);
      if (logs.status === 0 && (logs.stdout || logs.stderr)) {
        console.error(`[stage11] ${container} logs:\n${logs.stdout || ""}${logs.stderr || ""}`);
      }
    }
    throw error;
  } finally {
    for (const container of containers) removeContainer(container);
    fs.rmSync(tempRoot, { recursive: true, force: true });
  }
}

main().catch((error) => {
  console.error("[stage11] unattended production readiness failed:", error);
  process.exit(1);
});
