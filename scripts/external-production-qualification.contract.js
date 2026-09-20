"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const {
  MUTATION_APPROVAL,
  PAID_LLM_APPROVAL,
  PROFILE_APPROVAL,
  READ_ONLY_APPROVAL,
  executeQualification,
  hostnameApproved,
  isPrivateAddress,
  loadConfig,
  redact,
  runDeploymentChecks,
  runLLMCanary,
  runProfileCanary,
} = require("./external-production-qualification");

function baseEnvironment(overrides = {}) {
  return {
    AEGIS_LIVE_APPROVAL: READ_ONLY_APPROVAL,
    AEGIS_LIVE_BASE_URL: "https://crawler.example.test/",
    AEGIS_LIVE_ADMIN_API_KEY: "stage11b-admin-secret",
    ...overrides,
  };
}

function deploymentDependencies(overrides = {}) {
  const fetch = async (input, options = {}) => {
    const url = new URL(input);
    if (url.protocol === "http:") {
      return new Response(null, {
        status: 308,
        headers: { Location: `https://${url.hostname}${url.pathname}${url.search}` },
      });
    }
    if (url.pathname === "/health") {
      return Response.json({ status: "ok" });
    }
    if (url.pathname === "/admin/") {
      return new Response("<!doctype html><div id=\"root\"></div>", {
        headers: {
          "Strict-Transport-Security": "max-age=31536000; includeSubDomains",
          "X-Content-Type-Options": "nosniff",
          "X-Frame-Options": "DENY",
          "Content-Security-Policy": "default-src 'self'",
        },
      });
    }
    if (url.pathname === "/api/v1/capabilities") {
      return Response.json({
        features: { recordingV2: true, workflowV2: true, mcp: true },
        workerProtocolVersions: ["v1", "v2"],
      });
    }
    if (url.pathname === "/admin/mcp/tokens") {
      return options.headers?.Authorization === "Bearer stage11b-admin-secret"
        ? Response.json({ tokens: [] })
        : Response.json({ error: "unauthorized" }, { status: 401 });
    }
    if (url.pathname === "/metrics" && options.headers?.Authorization === "Bearer stage11b-metrics-secret") {
      return new Response("opencrawler_db_size_bytes 1024\n");
    }
    if (["/metrics", "/swagger.json", "/mcp", "/tasks/stage11b-auth-probe"].includes(url.pathname)) {
      return Response.json({ error: "unauthorized" }, { status: 401 });
    }
    throw new Error(`unexpected request ${url}`);
  };
  return {
    resolveDNS: async () => [{ address: "93.184.216.34", family: 4 }],
    inspectTLS: async () => ({
      authorized: true,
      protocol: "TLSv1.3",
      validTo: new Date(Date.now() + 45 * 86400000).toUTCString(),
      fingerprint256: "AA:BB:CC",
    }),
    fetch,
    ...overrides,
  };
}

test("loadConfig requires explicit read-only approval and HTTPS", () => {
  assert.throws(() => loadConfig(baseEnvironment({ AEGIS_LIVE_APPROVAL: "yes" })), /approval is required/);
  assert.throws(() => loadConfig(baseEnvironment({ AEGIS_LIVE_BASE_URL: "http:\/\/crawler.example.test" })), /must use https/);
  assert.throws(() => loadConfig(baseEnvironment({ AEGIS_LIVE_BASE_URL: "https:\/\/localhost" })), /public hostname/);
  const config = loadConfig(baseEnvironment());
  assert.equal(config.baseURL.toString(), "https://crawler.example.test/");
  assert.equal(config.httpBaseURL.toString(), "http://crawler.example.test/");
});

test("loadConfig reads secrets from files without allowing duplicate sources", () => {
  const env = baseEnvironment({
    AEGIS_LIVE_ADMIN_API_KEY: "",
    AEGIS_LIVE_ADMIN_API_KEY_FILE: "/run/secrets/admin",
  });
  const config = loadConfig(env, (fileName) => {
    assert.equal(fileName, "/run/secrets/admin");
    return "secret-from-file\n";
  });
  assert.equal(config.adminAPIKey, "secret-from-file");
  assert.throws(() => loadConfig(baseEnvironment({ AEGIS_LIVE_ADMIN_API_KEY_FILE: "/also-set" })), /set only one/);
});

test("paid LLM and authenticated profile canaries require exact separate consent", () => {
  assert.throws(() => loadConfig(baseEnvironment({ AEGIS_LIVE_RUN_LLM: "1" })), /paid LLM approval/);
  const llm = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_LLM: "1",
    AEGIS_LIVE_ALLOW_PAID_LLM: PAID_LLM_APPROVAL,
    AEGIS_LIVE_ALLOW_MUTATIONS: MUTATION_APPROVAL,
  }));
  assert.equal(llm.runLLM, true);

  const profileEnvironment = baseEnvironment({
    AEGIS_LIVE_RUN_PROFILE: "1",
    AEGIS_LIVE_ALLOW_AUTHENTICATED_TARGET: PROFILE_APPROVAL,
    AEGIS_LIVE_APPROVED_DOMAINS: "example.test",
    AEGIS_LIVE_TARGET_URL: "https://account.example.test/dashboard",
    AEGIS_LIVE_BROWSER_PROFILE_ID: "production-account",
    AEGIS_LIVE_BROWSER_PROFILE_DIR: "/profiles/production-account",
    AEGIS_LIVE_AUTH_PROOF_SELECTOR: "[data-authenticated=true]",
  });
  const profile = loadConfig(profileEnvironment);
  assert.equal(profile.profile.profileID, "production-account");
  assert.throws(() => loadConfig({
    ...profileEnvironment,
    AEGIS_LIVE_TARGET_URL: "https://example.test.attacker.invalid/dashboard",
  }), /outside AEGIS_LIVE_APPROVED_DOMAINS/);
});

test("domain and address policy reject suffix tricks and private networks", () => {
  assert.equal(hostnameApproved("account.example.test", ["example.test"]), true);
  assert.equal(hostnameApproved("example.test.attacker.invalid", ["example.test"]), false);
  for (const address of [
    "127.0.0.1", "10.1.2.3", "100.64.0.1", "172.16.0.1", "192.168.1.2",
    "192.0.2.1", "198.51.100.1", "203.0.113.10", "::1", "fd00::1", "fe80::1", "2001:db8::10",
    "::ffff:7f00:1", "100::1",
  ]) {
    assert.equal(isPrivateAddress(address), true, address);
  }
  assert.equal(isPrivateAddress("93.184.216.34"), false);
  assert.equal(isPrivateAddress("2606:2800:220:1:248:1893:25c8:1946"), false);
});

test("deployment qualification validates DNS, TLS, redirects, headers, capabilities, and admin auth", async () => {
  const config = loadConfig(baseEnvironment({ AEGIS_LIVE_METRICS_API_KEY: "stage11b-metrics-secret" }));
  const result = await runDeploymentChecks(config, deploymentDependencies());
  assert.equal(result.resolvedAddressCount, 1);
  assert.equal(result.tlsProtocol, "TLSv1.3");
  assert.equal(result.hstsMaxAge, 31536000);
  assert.equal(result.metricsChecked, true);
});

test("deployment qualification fails certificates that are too close to expiry", async () => {
  const config = loadConfig(baseEnvironment());
  await assert.rejects(() => runDeploymentChecks(config, deploymentDependencies({
    inspectTLS: async () => ({
      authorized: true,
      protocol: "TLSv1.3",
      validTo: new Date(Date.now() + 2 * 86400000).toUTCString(),
    }),
  })), /expires too soon/);
});

test("live LLM canary verifies audit metadata and always deletes its recording", async () => {
  const config = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_LLM: "1",
    AEGIS_LIVE_ALLOW_PAID_LLM: PAID_LLM_APPROVAL,
    AEGIS_LIVE_ALLOW_MUTATIONS: MUTATION_APPROVAL,
  }));
  const requests = [];
  const fetch = async (input, options = {}) => {
    const url = new URL(input);
    requests.push(`${options.method || "GET"} ${url.pathname}`);
    if (options.method === "POST" && url.pathname === "/api/v1/recordings") {
      const body = JSON.parse(options.body);
      assert.equal(body.recording.meta.source, "stage11b-live-canary");
      assert.match(body.recording.meta.canaryId, /^[0-9a-f-]{36}$/);
      return Response.json({ recording: { id: "recording-live" } }, { status: 201 });
    }
    if (options.method === "POST" && url.pathname.endsWith("/requirement-jobs")) {
      return Response.json({ job: { id: "job-live" } }, { status: 202 });
    }
    if (url.pathname === "/api/v1/requirement-jobs/job-live") {
      return Response.json({ job: {
        id: "job-live",
        status: "completed",
        source: "llm",
        provider: "openai-production",
        model: "production-model",
        inputTokens: 120,
        outputTokens: 80,
        result: {
          finalResponse: { cacheHit: false },
          candidates: [
            { requirement: { title: "Collect products" } },
            { requirement: { title: "Compare prices" } },
            { requirement: { title: "Monitor availability" } },
          ],
        },
      } });
    }
    if (options.method === "DELETE" && url.pathname === "/api/v1/recordings/recording-live") {
      return Response.json({ success: true });
    }
    throw new Error(`unexpected request ${options.method || "GET"} ${url.pathname}`);
  };
  const result = await runLLMCanary(config, { fetch, sleep: async () => {} });
  assert.equal(result.candidateCount, 3);
  assert.equal(requests.at(-1), "DELETE /api/v1/recordings/recording-live");
});

test("live LLM canary rejects cached output and still deletes its recording", async () => {
  const config = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_LLM: "1",
    AEGIS_LIVE_ALLOW_PAID_LLM: PAID_LLM_APPROVAL,
    AEGIS_LIVE_ALLOW_MUTATIONS: MUTATION_APPROVAL,
  }));
  let deleted = false;
  const fetch = async (input, options = {}) => {
    const url = new URL(input);
    if (options.method === "POST" && url.pathname === "/api/v1/recordings") {
      return Response.json({ recording: { id: "cached-recording" } }, { status: 201 });
    }
    if (options.method === "POST" && url.pathname.endsWith("/requirement-jobs")) {
      return Response.json({ job: { id: "cached-job" } }, { status: 202 });
    }
    if (url.pathname === "/api/v1/requirement-jobs/cached-job") {
      return Response.json({ job: {
        status: "completed",
        source: "llm",
        provider: "openai-production",
        model: "production-model",
        inputTokens: 100,
        outputTokens: 50,
        result: {
          finalResponse: { cacheHit: true },
          candidates: [
            { requirement: { title: "One" } },
            { requirement: { title: "Two" } },
            { requirement: { title: "Three" } },
          ],
        },
      } });
    }
    if (options.method === "DELETE" && url.pathname === "/api/v1/recordings/cached-recording") {
      deleted = true;
      return Response.json({ success: true });
    }
    throw new Error(`unexpected request ${options.method || "GET"} ${url.pathname}`);
  };
  await assert.rejects(() => runLLMCanary(config, { fetch, sleep: async () => {} }), /served from cache/);
  assert.equal(deleted, true);
});

test("live LLM canary reports only safe job failure metadata and still deletes its recording", async () => {
  const config = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_LLM: "1",
    AEGIS_LIVE_ALLOW_PAID_LLM: PAID_LLM_APPROVAL,
    AEGIS_LIVE_ALLOW_MUTATIONS: MUTATION_APPROVAL,
  }));
  const requests = [];
  const fetch = async (url, options = {}) => {
    const parsed = new URL(url);
    requests.push(`${options.method || "GET"} ${parsed.pathname}`);
    if (options.method === "POST" && parsed.pathname === "/api/v1/recordings") {
      return Response.json({ recording: { id: "failed-recording" } }, { status: 201 });
    }
    if (options.method === "POST" && parsed.pathname.endsWith("/requirement-jobs")) {
      return Response.json({ job: { id: "failed-job" } }, { status: 202 });
    }
    if (parsed.pathname === "/api/v1/requirement-jobs/failed-job") {
      return Response.json({ job: {
        id: "failed-job", status: "failed", errorCode: "INVALID_REQUIREMENT<script>",
        errorMessage: "provider output must stay private", attemptCount: 1,
      } });
    }
    if (options.method === "DELETE" && parsed.pathname === "/api/v1/recordings/failed-recording") {
      return new Response(null, { status: 204 });
    }
    throw new Error(`unexpected request ${options.method || "GET"} ${parsed.pathname}`);
  };

  await assert.rejects(
    () => runLLMCanary(config, { fetch, sleep: async () => {} }),
    (error) => error.code === "LIVE_LLM_FAILED"
      && error.message.includes("code INVALID_REQUIREMENTSCRIPT, attempts 1")
      && !error.message.includes("provider output"),
  );
  assert.equal(requests.at(-1), "DELETE /api/v1/recordings/failed-recording");
});

test("profile canary checks only visible authenticated state and closes the profile", async () => {
  const config = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_PROFILE: "1",
    AEGIS_LIVE_ALLOW_AUTHENTICATED_TARGET: PROFILE_APPROVAL,
    AEGIS_LIVE_APPROVED_DOMAINS: "example.test",
    AEGIS_LIVE_TARGET_URL: "https://account.example.test/dashboard",
    AEGIS_LIVE_BROWSER_PROFILE_ID: "production-account",
    AEGIS_LIVE_BROWSER_PROFILE_DIR: "/secure/profiles/production-account",
    AEGIS_LIVE_AUTH_PROOF_SELECTOR: "[data-authenticated=true]",
  }));
  const calls = [];
  const page = {
    goto: async (url) => calls.push(["goto", url]),
    url: () => "https://account.example.test/dashboard",
    locator: (selector) => {
      calls.push(["locator", selector]);
      return { first: () => ({ waitFor: async (options) => calls.push(["waitFor", options.state]) }) };
    },
  };
  const context = {
    pages: () => [page],
    newPage: async () => page,
    close: async () => calls.push(["close"]),
  };
  const result = await runProfileCanary(config, {
    realpath: () => "/secure/profiles/production-account",
    stat: () => ({ isDirectory: () => true }),
    playwright: { chromium: { launchPersistentContext: async () => context } },
  });
  assert.deepEqual(result, {
    browserProfileId: "production-account",
    finalHostname: "account.example.test",
    proofSelectorVisible: true,
  });
  assert.deepEqual(calls.map(([name]) => name), ["goto", "locator", "waitFor", "close"]);
});

test("profile errors do not expose profile paths or proof selectors", async () => {
  const profileDirectory = "/secure/profiles/confidential-account";
  const proofSelector = "[data-private-account-id=customer-42]";
  const config = loadConfig(baseEnvironment({
    AEGIS_LIVE_RUN_PROFILE: "1",
    AEGIS_LIVE_ALLOW_AUTHENTICATED_TARGET: PROFILE_APPROVAL,
    AEGIS_LIVE_APPROVED_DOMAINS: "example.test",
    AEGIS_LIVE_TARGET_URL: "https://account.example.test/dashboard",
    AEGIS_LIVE_BROWSER_PROFILE_ID: "production-account",
    AEGIS_LIVE_BROWSER_PROFILE_DIR: profileDirectory,
    AEGIS_LIVE_AUTH_PROOF_SELECTOR: proofSelector,
  }));
  await assert.rejects(() => runProfileCanary(config, {
    realpath: () => profileDirectory,
    stat: () => ({ isDirectory: () => true }),
    playwright: {
      chromium: {
        launchPersistentContext: async () => {
          throw new Error(`could not open ${profileDirectory} for ${proofSelector}`);
        },
      },
    },
  }), (error) => {
    assert.equal(error.code, "PROFILE_LAUNCH_FAILED");
    assert.equal(error.message.includes(profileDirectory), false);
    assert.equal(error.message.includes(proofSelector), false);
    return true;
  });
});

test("reports redact credentials and remain explicitly ineligible for promotion", async () => {
  const config = loadConfig(baseEnvironment());
  const report = await executeQualification(config, deploymentDependencies({
    inspectTLS: async () => {
      throw new Error(`upstream accidentally echoed ${config.adminAPIKey}`);
    },
  }));
  const serialized = JSON.stringify(report);
  assert.equal(report.status, "failed");
  assert.equal(report.promotionEligible, false);
  assert.equal(serialized.includes(config.adminAPIKey), false);
  assert.equal(serialized.includes("[REDACTED]"), true);
  assert.equal(report.blockers.length, 3);

  assert.deepEqual(redact({ nested: [`prefix-${config.adminAPIKey}-suffix`] }, [config.adminAPIKey]), {
    nested: ["prefix-[REDACTED]-suffix"],
  });
});
