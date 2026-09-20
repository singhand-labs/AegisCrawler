#!/usr/bin/env node
"use strict";

const crypto = require("crypto");
const dns = require("dns").promises;
const fs = require("fs");
const net = require("net");
const path = require("path");
const tls = require("tls");

const ROOT = path.resolve(__dirname, "..");
const READ_ONLY_APPROVAL = "I approve Stage 11B read-only production checks";
const PAID_LLM_APPROVAL = "I approve one paid Stage 11B LLM canary";
const MUTATION_APPROVAL = "I approve temporary Stage 11B records";
const PROFILE_APPROVAL = "I approve the Stage 11B authenticated profile canary";
const REPORT_SCHEMA = "aegiscrawler.stage11b.qualification.v1";

class QualificationError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "QualificationError";
    this.code = code;
  }
}

function assert(condition, code, message) {
  if (!condition) throw new QualificationError(code, message);
}

function parseFlag(env, name) {
  const value = (env[name] || "").trim();
  assert(value === "" || value === "0" || value === "1", "INVALID_CONFIGURATION", `${name} must be 0 or 1`);
  return value === "1";
}

function readSecret(env, name, readFile = fs.readFileSync) {
  const direct = env[name] || "";
  const fileName = (env[`${name}_FILE`] || "").trim();
  assert(!(direct && fileName), "INVALID_CONFIGURATION", `set only one of ${name} and ${name}_FILE`);
  if (fileName) {
    const value = readFile(fileName, "utf8").trim();
    assert(value, "MISSING_CONFIGURATION", `${name}_FILE is empty`);
    return value;
  }
  assert(direct, "MISSING_CONFIGURATION", `${name} or ${name}_FILE is required`);
  return direct;
}

function parseURL(value, protocol, name) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new QualificationError("INVALID_CONFIGURATION", `${name} must be an absolute URL`);
  }
  assert(parsed.protocol === protocol, "INVALID_CONFIGURATION", `${name} must use ${protocol}`);
  assert(!parsed.username && !parsed.password, "INVALID_CONFIGURATION", `${name} must not contain credentials`);
  assert(!parsed.search && !parsed.hash, "INVALID_CONFIGURATION", `${name} must not contain a query or fragment`);
  if (!parsed.pathname.endsWith("/")) parsed.pathname += "/";
  return parsed;
}

function parsePositiveInteger(value, fallback, name, maximum) {
  if (value === undefined || value === "") return fallback;
  const parsed = Number(value);
  assert(Number.isInteger(parsed) && parsed > 0 && parsed <= maximum, "INVALID_CONFIGURATION", `${name} is invalid`);
  return parsed;
}

function isLocalHostname(hostname) {
  const lower = hostname.toLowerCase();
  return lower === "localhost" || lower.endsWith(".localhost") || lower.endsWith(".local");
}

function isPrivateAddress(address) {
  const normalized = address.toLowerCase();
  if (normalized === "::1" || normalized === "::" || normalized.startsWith("fc") || normalized.startsWith("fd")) return true;
  if (/^fe[89ab]/.test(normalized)) return true;
  if (normalized.startsWith("ff") || normalized.startsWith("2001:db8:") || normalized.startsWith("2001:0db8:")) return true;
  let mapped = normalized.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/)?.[1];
  const mappedHex = normalized.match(/^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/);
  if (!mapped && mappedHex) {
    const high = Number.parseInt(mappedHex[1], 16);
    const low = Number.parseInt(mappedHex[2], 16);
    mapped = `${high >> 8}.${high & 255}.${low >> 8}.${low & 255}`;
  }
  const ipv4 = mapped || (net.isIP(normalized) === 4 ? normalized : "");
  if (!ipv4) {
    if (net.isIP(normalized) !== 6) return true;
    const firstHextet = Number.parseInt(normalized.split(":", 1)[0], 16);
    return !Number.isFinite(firstHextet) || firstHextet < 0x2000 || firstHextet > 0x3fff;
  }
  const octets = ipv4.split(".").map(Number);
  return octets[0] === 0
    || octets[0] === 10
    || (octets[0] === 100 && octets[1] >= 64 && octets[1] <= 127)
    || octets[0] === 127
    || (octets[0] === 169 && octets[1] === 254)
    || (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31)
    || (octets[0] === 192 && octets[1] === 0)
    || (octets[0] === 192 && octets[1] === 168)
    || (octets[0] === 198 && (octets[1] === 18 || octets[1] === 19))
    || (octets[0] === 198 && octets[1] === 51 && octets[2] === 100)
    || (octets[0] === 203 && octets[1] === 0 && octets[2] === 113)
    || octets[0] >= 224;
}

function hostnameApproved(hostname, approvedDomains) {
  const lower = hostname.toLowerCase();
  return approvedDomains.some((domain) => lower === domain || lower.endsWith(`.${domain}`));
}

function loadConfig(env = process.env, readFile = fs.readFileSync) {
  assert(env.AEGIS_LIVE_APPROVAL === READ_ONLY_APPROVAL, "APPROVAL_REQUIRED", "read-only Stage 11B approval is required");
  const baseURL = parseURL((env.AEGIS_LIVE_BASE_URL || "").trim(), "https:", "AEGIS_LIVE_BASE_URL");
  assert(!isLocalHostname(baseURL.hostname), "INVALID_CONFIGURATION", "AEGIS_LIVE_BASE_URL must use a public hostname");
  assert(baseURL.port === "" || baseURL.port === "443" || parseFlag(env, "AEGIS_LIVE_ALLOW_NONSTANDARD_HTTPS_PORT"),
    "INVALID_CONFIGURATION", "non-standard HTTPS ports require AEGIS_LIVE_ALLOW_NONSTANDARD_HTTPS_PORT=1");

  const httpBaseURL = env.AEGIS_LIVE_HTTP_BASE_URL
    ? parseURL(env.AEGIS_LIVE_HTTP_BASE_URL.trim(), "http:", "AEGIS_LIVE_HTTP_BASE_URL")
    : new URL(`http://${baseURL.hostname}${baseURL.pathname}`);
  assert(httpBaseURL.hostname === baseURL.hostname, "INVALID_CONFIGURATION", "HTTP and HTTPS qualification hosts must match");

  const adminAPIKey = readSecret(env, "AEGIS_LIVE_ADMIN_API_KEY", readFile);
  const metricsAPIKey = env.AEGIS_LIVE_METRICS_API_KEY || env.AEGIS_LIVE_METRICS_API_KEY_FILE
    ? readSecret(env, "AEGIS_LIVE_METRICS_API_KEY", readFile)
    : "";
  const runLLM = parseFlag(env, "AEGIS_LIVE_RUN_LLM");
  const runProfile = parseFlag(env, "AEGIS_LIVE_RUN_PROFILE");

  if (runLLM) {
    assert(env.AEGIS_LIVE_ALLOW_PAID_LLM === PAID_LLM_APPROVAL, "APPROVAL_REQUIRED", "paid LLM approval is required");
    assert(env.AEGIS_LIVE_ALLOW_MUTATIONS === MUTATION_APPROVAL, "APPROVAL_REQUIRED", "temporary-record mutation approval is required");
  }

  let profile;
  if (runProfile) {
    assert(env.AEGIS_LIVE_ALLOW_AUTHENTICATED_TARGET === PROFILE_APPROVAL, "APPROVAL_REQUIRED", "authenticated-profile approval is required");
    const approvedDomains = (env.AEGIS_LIVE_APPROVED_DOMAINS || "")
      .split(",")
      .map((value) => value.trim().toLowerCase().replace(/^\./, ""))
      .filter(Boolean);
    assert(approvedDomains.length > 0, "MISSING_CONFIGURATION", "AEGIS_LIVE_APPROVED_DOMAINS is required for the profile canary");
    const targetURL = parseURL((env.AEGIS_LIVE_TARGET_URL || "").trim(), "https:", "AEGIS_LIVE_TARGET_URL");
    assert(hostnameApproved(targetURL.hostname, approvedDomains), "DOMAIN_NOT_APPROVED", "the profile target is outside AEGIS_LIVE_APPROVED_DOMAINS");
    const profileID = (env.AEGIS_LIVE_BROWSER_PROFILE_ID || "").trim();
    const profileDirectory = (env.AEGIS_LIVE_BROWSER_PROFILE_DIR || "").trim();
    const proofSelector = (env.AEGIS_LIVE_AUTH_PROOF_SELECTOR || "").trim();
    assert(profileID && profileDirectory && proofSelector, "MISSING_CONFIGURATION",
      "profile ID, profile directory, and auth proof selector are required");
    profile = {
      approvedDomains,
      targetURL,
      profileID,
      profileDirectory,
      proofSelector,
      headless: !parseFlag(env, "AEGIS_LIVE_PROFILE_HEADFUL"),
    };
  }

  return {
    baseURL,
    httpBaseURL,
    adminAPIKey,
    metricsAPIKey,
    minCertificateDays: parsePositiveInteger(env.AEGIS_LIVE_MIN_CERT_DAYS, 14, "AEGIS_LIVE_MIN_CERT_DAYS", 365),
    requestTimeoutMs: parsePositiveInteger(env.AEGIS_LIVE_REQUEST_TIMEOUT_MS, 15000, "AEGIS_LIVE_REQUEST_TIMEOUT_MS", 300000),
    jobTimeoutMs: parsePositiveInteger(env.AEGIS_LIVE_JOB_TIMEOUT_MS, 120000, "AEGIS_LIVE_JOB_TIMEOUT_MS", 600000),
    runLLM,
    runProfile,
    profile,
    reportPath: (env.AEGIS_LIVE_REPORT_PATH || "").trim(),
  };
}

function routeURL(baseURL, route) {
  return new URL(route.replace(/^\//, ""), baseURL);
}

async function fetchWithTimeout(fetchImpl, url, options, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetchImpl(url, { ...options, signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function inspectTLS({ hostname, port, timeoutMs }) {
  return new Promise((resolve, reject) => {
    const socket = tls.connect({
      host: hostname,
      port,
      servername: hostname,
      rejectUnauthorized: true,
      minVersion: "TLSv1.2",
    });
    const timer = setTimeout(() => socket.destroy(new Error("TLS connection timed out")), timeoutMs);
    socket.once("secureConnect", () => {
      clearTimeout(timer);
      const certificate = socket.getPeerCertificate();
      const result = {
        authorized: socket.authorized,
        protocol: socket.getProtocol(),
        validTo: certificate.valid_to,
        fingerprint256: certificate.fingerprint256,
      };
      socket.end();
      resolve(result);
    });
    socket.once("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
}

async function expectStatus(response, expected, code, message) {
  assert(response.status === expected, code, `${message} (HTTP ${response.status})`);
}

async function runDeploymentChecks(config, dependencies = {}) {
  const resolveDNS = dependencies.resolveDNS || ((hostname) => dns.lookup(hostname, { all: true }));
  const tlsInspector = dependencies.inspectTLS || inspectTLS;
  const fetchImpl = dependencies.fetch || globalThis.fetch;
  const addresses = await resolveDNS(config.baseURL.hostname);
  assert(Array.isArray(addresses) && addresses.length > 0, "DNS_FAILED", "public hostname did not resolve");
  assert(addresses.every(({ address }) => net.isIP(address) && !isPrivateAddress(address)),
    "DNS_NOT_PUBLIC", "public hostname resolved to a private or invalid address");

  const tlsResult = await tlsInspector({
    hostname: config.baseURL.hostname,
    port: Number(config.baseURL.port || 443),
    timeoutMs: config.requestTimeoutMs,
  });
  assert(tlsResult.authorized, "TLS_UNTRUSTED", "TLS certificate is not trusted");
  assert(tlsResult.protocol === "TLSv1.2" || tlsResult.protocol === "TLSv1.3", "TLS_PROTOCOL", "TLS 1.2 or newer is required");
  const validTo = Date.parse(tlsResult.validTo || "");
  assert(Number.isFinite(validTo), "TLS_CERTIFICATE", "TLS certificate expiry is unavailable");
  const certificateDaysRemaining = Math.floor((validTo - Date.now()) / 86400000);
  assert(certificateDaysRemaining >= config.minCertificateDays, "TLS_EXPIRY", "TLS certificate expires too soon");

  const redirect = await fetchWithTimeout(fetchImpl, routeURL(config.httpBaseURL, "/health?ready=1"), { redirect: "manual" }, config.requestTimeoutMs);
  assert([301, 308].includes(redirect.status), "HTTPS_REDIRECT", `plain HTTP did not permanently redirect (HTTP ${redirect.status})`);
  const redirectLocation = redirect.headers.get("location");
  assert(redirectLocation, "HTTPS_REDIRECT", "plain HTTP redirect omitted Location");
  const redirectURL = new URL(redirectLocation, config.httpBaseURL);
  assert(redirectURL.protocol === "https:" && redirectURL.hostname === config.baseURL.hostname,
    "HTTPS_REDIRECT", "plain HTTP redirected outside the production HTTPS host");

  const ready = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/health?ready=1"), {}, config.requestTimeoutMs);
  await expectStatus(ready, 200, "READINESS_FAILED", "database readiness failed");
  const readyBody = await ready.json();
  assert(readyBody.status === "ok", "READINESS_FAILED", "database readiness returned a non-ok status");

  const admin = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/admin/"), { headers: { Accept: "text/html" } }, config.requestTimeoutMs);
  await expectStatus(admin, 200, "ADMIN_UNAVAILABLE", "Admin UI is unavailable");
  const hstsMaxAge = Number((admin.headers.get("strict-transport-security") || "").match(/(?:^|;)\s*max-age=(\d+)/i)?.[1]);
  assert(Number.isFinite(hstsMaxAge) && hstsMaxAge >= 15552000, "SECURITY_HEADERS", "HSTS max-age is missing or shorter than 180 days");
  assert((admin.headers.get("x-content-type-options") || "").toLowerCase() === "nosniff", "SECURITY_HEADERS", "X-Content-Type-Options is missing");
  assert((admin.headers.get("x-frame-options") || "").toUpperCase() === "DENY", "SECURITY_HEADERS", "X-Frame-Options is missing or permissive");
  assert((admin.headers.get("content-security-policy") || "").includes("default-src 'self'"), "SECURITY_HEADERS", "Content-Security-Policy has no self-only default");
  const adminBody = await admin.text();
  assert(adminBody.includes('<div id="root"></div>') && !adminBody.includes("管理后台未构建"),
    "ADMIN_UNAVAILABLE", "deployed Admin UI is an unbuilt placeholder");

  const capabilities = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/api/v1/capabilities"), {}, config.requestTimeoutMs);
  await expectStatus(capabilities, 200, "CAPABILITIES_FAILED", "capability discovery failed");
  const capabilityBody = await capabilities.json();
  assert(capabilityBody.features?.recordingV2 === true
    && capabilityBody.features?.workflowV2 === true
    && capabilityBody.features?.mcp === true
    && capabilityBody.workerProtocolVersions?.includes("v2"),
  "CAPABILITIES_FAILED", "required production capabilities are not enabled");

  const tokenURL = routeURL(config.baseURL, "/admin/mcp/tokens");
  const unauthenticated = await fetchWithTimeout(fetchImpl, tokenURL, {}, config.requestTimeoutMs);
  await expectStatus(unauthenticated, 401, "ADMIN_FAIL_OPEN", "Admin API did not reject an unauthenticated request");
  const invalid = await fetchWithTimeout(fetchImpl, tokenURL, { headers: { Authorization: "Bearer stage11b-invalid-token" } }, config.requestTimeoutMs);
  await expectStatus(invalid, 401, "ADMIN_FAIL_OPEN", "Admin API accepted an invalid token");
  const authenticated = await fetchWithTimeout(fetchImpl, tokenURL, {
    headers: { Authorization: `Bearer ${config.adminAPIKey}` },
  }, config.requestTimeoutMs);
  await expectStatus(authenticated, 200, "ADMIN_AUTH_FAILED", "Admin API rejected the configured credential");

  const unauthenticatedMetrics = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/metrics"), {}, config.requestTimeoutMs);
  await expectStatus(unauthenticatedMetrics, 401, "OBSERVABILITY_FAIL_OPEN", "metrics did not reject an unauthenticated request");
  if (config.metricsAPIKey) {
    const metrics = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/metrics"), {
      headers: { Authorization: `Bearer ${config.metricsAPIKey}` },
    }, config.requestTimeoutMs);
    await expectStatus(metrics, 200, "METRICS_FAILED", "authenticated metrics scrape failed");
  }

  const unauthenticatedSwagger = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/swagger.json"), {}, config.requestTimeoutMs);
  await expectStatus(unauthenticatedSwagger, 401, "SWAGGER_FAIL_OPEN", "Swagger did not reject an unauthenticated request");
  const unauthenticatedMCP = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/mcp"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: {} }),
  }, config.requestTimeoutMs);
  await expectStatus(unauthenticatedMCP, 401, "MCP_FAIL_OPEN", "MCP did not reject an unauthenticated request");
  const unauthenticatedWorker = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, "/tasks/stage11b-auth-probe"), {}, config.requestTimeoutMs);
  await expectStatus(unauthenticatedWorker, 401, "WORKER_FAIL_OPEN", "worker API did not reject an unauthenticated request");

  return {
    resolvedAddressCount: addresses.length,
    tlsProtocol: tlsResult.protocol,
    certificateDaysRemaining,
    certificateFingerprint256: tlsResult.fingerprint256 || "unavailable",
    hstsMaxAge,
    metricsChecked: Boolean(config.metricsAPIKey),
  };
}

function liveRecording() {
  return {
    version: "2.0.0",
    meta: {
      startUrl: "https://example.invalid/stage11b",
      source: "stage11b-live-canary",
      canaryId: crypto.randomUUID(),
    },
    events: [{ type: "click", selector: "#collect", timestamp: 1 }],
    snapshots: [
      { phase: "initial", actionIndex: 0, url: "https://example.invalid/stage11b", text: "Product Alpha price 10" },
      { phase: "final", actionIndex: 1, url: "https://example.invalid/stage11b", text: "Product Alpha price 10 collected" },
    ],
  };
}

async function apiRequest(config, fetchImpl, method, route, body) {
  const response = await fetchWithTimeout(fetchImpl, routeURL(config.baseURL, route), {
    method,
    headers: {
      Authorization: `Bearer ${config.adminAPIKey}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  }, config.requestTimeoutMs);
  assert(response.ok, "LIVE_API_FAILED", `${method} ${route} failed (HTTP ${response.status})`);
  return response.status === 204 ? {} : response.json();
}

async function runLLMCanary(config, dependencies = {}) {
  const fetchImpl = dependencies.fetch || globalThis.fetch;
  const sleep = dependencies.sleep || ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
  let recordingID = "";
  try {
    const created = await apiRequest(config, fetchImpl, "POST", "/api/v1/recordings", { recording: liveRecording() });
    recordingID = created.recording?.id || "";
    assert(recordingID, "LIVE_LLM_FAILED", "live canary recording creation omitted its ID");
    const submitted = await apiRequest(config, fetchImpl, "POST", `/api/v1/recordings/${encodeURIComponent(recordingID)}/requirement-jobs`);
    const jobID = submitted.job?.id || "";
    assert(jobID, "LIVE_LLM_FAILED", "candidate job submission omitted its ID");

    const deadline = Date.now() + config.jobTimeoutMs;
    let job;
    while (Date.now() < deadline) {
      const result = await apiRequest(config, fetchImpl, "GET", `/api/v1/requirement-jobs/${encodeURIComponent(jobID)}`);
      job = result.job;
      if (job?.status === "completed" || job?.status === "failed") break;
      await sleep(1000);
    }
    const jobStatus = job?.status || "timeout";
    const errorCode = String(job?.errorCode || "UNKNOWN").toUpperCase().replace(/[^A-Z0-9_]/g, "").slice(0, 64) || "UNKNOWN";
    const attemptCount = Number.isInteger(job?.attemptCount) ? job.attemptCount : 0;
    assert(jobStatus === "completed", "LIVE_LLM_FAILED",
      `candidate job ended as ${jobStatus} (code ${errorCode}, attempts ${attemptCount})`);
    assert(job.source === "llm" && job.provider && !["manual", "deterministic", "fake"].includes(job.provider),
      "LIVE_LLM_FAILED", "candidate job did not use a live provider");
    assert(job.model && job.inputTokens > 0 && job.outputTokens > 0, "LIVE_LLM_AUDIT", "live provider audit metadata is incomplete");
    const candidates = job.result?.candidates;
    assert(Array.isArray(candidates) && candidates.length === 3, "LIVE_LLM_FAILED", "live provider did not return exactly three candidates");
    assert(job.result?.finalResponse?.cacheHit === false, "LIVE_LLM_CACHED", "live provider canary was served from cache");
    const titles = candidates.map((candidate) => candidate.requirement?.title?.trim().toLowerCase()).filter(Boolean);
    assert(new Set(titles).size === 3, "LIVE_LLM_FAILED", "live provider candidates are not distinct");
    return {
      jobID,
      provider: job.provider,
      model: job.model,
      inputTokens: job.inputTokens,
      outputTokens: job.outputTokens,
      candidateCount: candidates.length,
    };
  } finally {
    if (recordingID) {
      await apiRequest(config, fetchImpl, "DELETE", `/api/v1/recordings/${encodeURIComponent(recordingID)}`);
    }
  }
}

async function runProfileCanary(config, dependencies = {}) {
  const profile = config.profile;
  assert(profile, "MISSING_CONFIGURATION", "profile canary configuration is unavailable");
  const realpath = dependencies.realpath || fs.realpathSync;
  const stat = dependencies.stat || fs.statSync;
  let profileDirectory;
  try {
    profileDirectory = realpath(profile.profileDirectory);
    assert(stat(profileDirectory).isDirectory(), "PROFILE_UNAVAILABLE", "browser profile path is not a directory");
  } catch (error) {
    if (error instanceof QualificationError) throw error;
    throw new QualificationError("PROFILE_UNAVAILABLE", "browser profile directory could not be opened");
  }
  assert(profileDirectory !== ROOT && !profileDirectory.startsWith(`${ROOT}${path.sep}`),
    "PROFILE_UNSAFE_PATH", "browser profile directory must be outside the repository");
  const playwright = dependencies.playwright || require("playwright");
  let context;
  try {
    context = await playwright.chromium.launchPersistentContext(profileDirectory, { headless: profile.headless });
  } catch {
    throw new QualificationError("PROFILE_LAUNCH_FAILED", "persistent browser profile could not be launched");
  }
  try {
    const page = context.pages()[0] || await context.newPage();
    try {
      await page.goto(profile.targetURL.toString(), { waitUntil: "domcontentloaded", timeout: config.requestTimeoutMs });
    } catch {
      throw new QualificationError("PROFILE_NAVIGATION_FAILED", "authenticated profile target could not be loaded");
    }
    const finalURL = new URL(page.url());
    assert(finalURL.protocol === "https:" && hostnameApproved(finalURL.hostname, profile.approvedDomains),
      "DOMAIN_NOT_APPROVED", "authenticated profile redirected outside approved domains");
    try {
      await page.locator(profile.proofSelector).first().waitFor({ state: "visible", timeout: config.requestTimeoutMs });
    } catch {
      throw new QualificationError("PROFILE_AUTH_PROOF_FAILED", "authenticated-state proof was not visible");
    }
    return { browserProfileId: profile.profileID, finalHostname: finalURL.hostname, proofSelectorVisible: true };
  } finally {
    try {
      await context.close();
    } catch {
      throw new QualificationError("PROFILE_CLOSE_FAILED", "persistent browser profile did not close cleanly");
    }
  }
}

function redact(value, secrets) {
  if (typeof value === "string") {
    return secrets.filter(Boolean).reduce((text, secret) => text.split(secret).join("[REDACTED]"), value);
  }
  if (Array.isArray(value)) return value.map((item) => redact(item, secrets));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, redact(item, secrets)]));
  }
  return value;
}

async function phase(name, operation) {
  const startedAt = new Date().toISOString();
  try {
    const details = await operation();
    return { name, status: "passed", startedAt, finishedAt: new Date().toISOString(), details };
  } catch (error) {
    return {
      name,
      status: "failed",
      startedAt,
      finishedAt: new Date().toISOString(),
      error: { code: error.code || "UNEXPECTED_ERROR", message: error.message || String(error) },
    };
  }
}

async function executeQualification(config, dependencies = {}) {
  const startedAt = new Date().toISOString();
  const phases = [await phase("public-deployment", () => runDeploymentChecks(config, dependencies))];
  if (config.runLLM) phases.push(await phase("live-llm", () => runLLMCanary(config, dependencies)));
  if (config.runProfile) phases.push(await phase("authenticated-profile-health", () => runProfileCanary(config, dependencies)));
  const failed = phases.some((item) => item.status === "failed");
  const report = {
    schema: REPORT_SCHEMA,
    status: failed ? "failed" : "passed",
    promotionEligible: false,
    startedAt,
    finishedAt: new Date().toISOString(),
    target: { hostname: config.baseURL.hostname },
    selectedGates: { liveLLM: config.runLLM, authenticatedProfile: config.runProfile },
    phases,
    blockers: [
      "named-profile claim affinity is implemented, but the deployed worker-to-profile mapping still requires a real profile-bound pilot task",
      "resumable operator checkpoints are implemented, but a real authorized human-intervention pilot is still required",
      "final pilot and production promotion require an authorized human decision",
    ],
  };
  return redact(report, [config.adminAPIKey, config.metricsAPIKey]);
}

async function main() {
  let config;
  try {
    config = loadConfig();
    const report = await executeQualification(config);
    const serialized = `${JSON.stringify(report, null, 2)}\n`;
    if (config.reportPath) {
      const output = path.resolve(config.reportPath);
      fs.writeFileSync(output, serialized, { encoding: "utf8", mode: 0o600, flag: "wx" });
      console.log(`[stage11b] redacted report written to ${output}`);
    } else {
      process.stdout.write(serialized);
    }
    if (report.status !== "passed") process.exitCode = 1;
  } catch (error) {
    const safe = redact({
      schema: REPORT_SCHEMA,
      status: "failed",
      promotionEligible: false,
      error: { code: error.code || "UNEXPECTED_ERROR", message: error.message || String(error) },
    }, config ? [config.adminAPIKey, config.metricsAPIKey] : []);
    process.stderr.write(`${JSON.stringify(safe, null, 2)}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  MUTATION_APPROVAL,
  PAID_LLM_APPROVAL,
  PROFILE_APPROVAL,
  QualificationError,
  READ_ONLY_APPROVAL,
  executeQualification,
  hostnameApproved,
  isPrivateAddress,
  liveRecording,
  loadConfig,
  redact,
  routeURL,
  runDeploymentChecks,
  runLLMCanary,
  runProfileCanary,
};

if (require.main === module) {
  main();
}
