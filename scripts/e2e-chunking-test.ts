import * as os from 'os';
import * as path from 'path';
import { ChildProcess } from 'child_process';
import { createServer as createHttpServer, IncomingMessage, Server, ServerResponse } from 'http';
import {
  ADMIN_API_KEY,
  buildServer,
  getFreePort,
  killServer,
  removeIfExists,
  startServer,
  waitForHealth,
} from './e2e-deployment-test';

// Hermetic chunking acceptance: a synthetic multi-snapshot recording larger
// than the configured LLM input budget must be analyzed as ordered chunks,
// with no snapshot omitted, followed by exactly one synthesis call. The job
// API must expose the PR #49 chunk progress fields, and the completed
// candidates payload must be schema-valid.

const SNAPSHOT_COUNT = 8;
// budgetBytes = LLM_MAX_INPUT_TOKENS * 3 (server buildPromptChunks), so 1000
// tokens budgets 3000 bytes and forces the ~8 KB synthetic recording into
// multiple ordered chunks.
const LLM_MAX_INPUT_TOKENS = '1000';
const JOB_TIMEOUT_MS = 60_000;
const POLL_INTERVAL_MS = 100;
// Slow each fake chunk-analysis response just enough for the job API poller
// to observe intermediate completedChunks progress while the job runs.
const CHUNK_ANALYSIS_LATENCY_MS = 150;

interface OpenAIMessage {
  role: string;
  content: string;
}

interface OpenAIRequest {
  messages?: OpenAIMessage[];
}

interface ChunkAnalysisObservation {
  index: number;
  total: number;
  snapshotIndices: number[];
}

interface FakeLLMObservations {
  chunkAnalyses: ChunkAnalysisObservation[];
  synthesisRequests: number;
  fullRecordingRequests: number;
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function readRequestBody(request: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    request.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    request.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    request.on('error', reject);
  });
}

function listen(server: Server, port: number): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve());
  });
}

function closeServer(server: Server | undefined): Promise<void> {
  return new Promise((resolve) => {
    if (!server?.listening) {
      resolve();
      return;
    }
    server.close(() => resolve());
  });
}

function chunkFixtureRecording(): Record<string, unknown> {
  const snapshots: Record<string, unknown>[] = [];
  const events: Record<string, unknown>[] = [];
  for (let index = 0; index < SNAPSHOT_COUNT; index += 1) {
    const phase = index === 0 ? 'initial' : index === SNAPSHOT_COUNT - 1 ? 'final' : 'before-action';
    snapshots.push({
      phase,
      actionIndex: index,
      url: 'http://127.0.0.1/chunk-fixture/items',
      marker: `chunk-snapshot-marker-${index}`,
      title: `Chunk fixture snapshot ${index}`,
      // ~700 bytes of distinct sanitized text per snapshot so the recording
      // exceeds the configured token budget and must be chunked.
      text: `Visible fixture items for snapshot ${index}: `.padEnd(700, `item-${index}-alpha beta gamma delta `.slice(0, 40)),
    });
    if (index < SNAPSHOT_COUNT - 1) {
      events.push({ type: 'click', actionIndex: index, selector: `#load-more-${index}`, timestamp: index + 1 });
    }
  }
  return {
    version: '2.0.0',
    meta: { startUrl: 'http://127.0.0.1/chunk-fixture/items', source: 'e2e-chunking-test' },
    events,
    snapshots,
  };
}

function requirementCandidates(): Record<string, unknown> {
  const requirement = (title: string) => ({
    title,
    description: 'Collect the visible fixture items produced by the recorded chunked session.',
    requiredInputs: [],
    optionalInputs: [],
    outputFields: [{ name: 'result', type: 'string', description: 'Visible fixture result' }],
    sampleOutput: { result: 'chunked fixture result' },
  });
  return {
    candidates: [
      { id: 'c1', confidence: 0.91, requirement: requirement('Collect visible fixture items') },
      { id: 'c2', confidence: 0.82, requirement: requirement('Verify recorded chunk interactions') },
      { id: 'c3', confidence: 0.71, requirement: requirement('Monitor fixture item changes') },
    ],
  };
}

async function startFakeLLM(port: number): Promise<{ server: Server; observations: FakeLLMObservations }> {
  const observations: FakeLLMObservations = { chunkAnalyses: [], synthesisRequests: 0, fullRecordingRequests: 0 };
  const server = createHttpServer(async (request: IncomingMessage, response: ServerResponse) => {
    if (request.method !== 'POST' || request.url !== '/v1/chat/completions') {
      response.writeHead(404).end();
      return;
    }
    const body = JSON.parse(await readRequestBody(request)) as OpenAIRequest;
    const system = body.messages?.find((message) => message.role === 'system')?.content ?? '';
    const user = body.messages?.find((message) => message.role === 'user')?.content ?? '';
    let content: Record<string, unknown>;
    if (system.includes('You analyze sanitized browser recordings')) {
      const header = user.match(/^Chunk (\d+) of (\d+)\./);
      assert(header, `chunk analysis request omitted the ordered chunk header: ${user.slice(0, 120)}`);
      const snapshotIndices = [...user.matchAll(/"kind":"snapshot","index":(\d+)/g)].map((match) => Number(match[1]));
      observations.chunkAnalyses.push({ index: Number(header[1]), total: Number(header[2]), snapshotIndices });
      content = { summary: `chunk ${header[1]} analyzed`, observedInputs: [], observedOutputs: ['result'], safetyFlags: [] };
      await sleep(CHUNK_ANALYSIS_LATENCY_MS);
    } else if (system.includes('design safe web collection requirements')) {
      if (user.includes('Synthesize exactly three distinct candidates using every ordered chunk analysis')) {
        observations.synthesisRequests += 1;
      } else if (user.includes('Generate exactly three distinct candidates from this complete sanitized recording')) {
        observations.fullRecordingRequests += 1;
      }
      content = requirementCandidates();
    } else {
      response.writeHead(400, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify({ error: { message: `unexpected fake LLM request: ${system.slice(0, 120)}` } }));
      return;
    }
    response.writeHead(200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify({
      choices: [{ message: { content: JSON.stringify(content) } }],
      usage: { prompt_tokens: 100, completion_tokens: 50 },
    }));
  });
  await listen(server, port);
  return { server, observations };
}

function serverEnvironment(fakeLLMPort: number): Record<string, string> {
  return {
    FEATURE_RECORDING_V2: 'true',
    FEATURE_WORKFLOW_V2: 'true',
    FEATURE_WORKER_PROTOCOL_V2: 'true',
    FEATURE_MCP: 'true',
    LLM_ENABLED: 'true',
    LLM_PROVIDER: 'openai',
    LLM_API_KEY: 'chunk-e2e-fake-key',
    LLM_BASE_URL: `http://127.0.0.1:${fakeLLMPort}/v1`,
    LLM_MODEL: 'chunk-e2e-fake-model',
    LLM_REQUEST_TIMEOUT: '10s',
    LLM_MAX_INPUT_TOKENS,
    LLM_JOB_WORKER_INTERVAL: '50ms',
    RATE_LIMIT_PER_SECOND: '2000',
    RATE_LIMIT_BURST: '4000',
  };
}

async function apiJSON<T>(baseUrl: string, method: string, route: string, body?: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${ADMIN_API_KEY}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) throw new Error(`${method} ${route} returned ${response.status}: ${text}`);
  return text ? JSON.parse(text) as T : {} as T;
}

function assertValidCandidates(candidates: any): void {
  assert(Array.isArray(candidates) && candidates.length === 3,
    `candidates payload did not contain exactly three candidates: ${JSON.stringify(candidates)?.slice(0, 400)}`);
  const titles = new Set<string>();
  for (const [index, candidate] of candidates.entries()) {
    assert(typeof candidate.id === 'string' && candidate.id.length > 0, `candidate ${index + 1} omitted its id`);
    assert(typeof candidate.confidence === 'number' && candidate.confidence >= 0 && candidate.confidence <= 1,
      `candidate ${index + 1} confidence is out of range: ${candidate.confidence}`);
    const requirement = candidate.requirement;
    assert(requirement && typeof requirement === 'object', `candidate ${index + 1} omitted its requirement`);
    assert(typeof requirement.title === 'string' && requirement.title.trim().length > 0, `candidate ${index + 1} omitted a title`);
    assert(typeof requirement.description === 'string' && requirement.description.trim().length > 0,
      `candidate ${index + 1} omitted a description`);
    assert(Array.isArray(requirement.requiredInputs), `candidate ${index + 1} requiredInputs is not an array`);
    assert(Array.isArray(requirement.optionalInputs), `candidate ${index + 1} optionalInputs is not an array`);
    assert(Array.isArray(requirement.outputFields) && requirement.outputFields.length > 0,
      `candidate ${index + 1} omitted output fields`);
    for (const field of requirement.outputFields) {
      assert(typeof field.name === 'string' && field.name.length > 0, `candidate ${index + 1} has an unnamed output field`);
      assert(typeof field.type === 'string' && field.type.length > 0, `candidate ${index + 1} output field ${field.name} omitted its type`);
      assert(typeof field.description === 'string' && field.description.length > 0,
        `candidate ${index + 1} output field ${field.name} omitted its description`);
    }
    assert(requirement.sampleOutput && typeof requirement.sampleOutput === 'object' && !Array.isArray(requirement.sampleOutput),
      `candidate ${index + 1} sampleOutput is not an object`);
    assert(
      JSON.stringify(Object.keys(requirement.sampleOutput).sort())
        === JSON.stringify(requirement.outputFields.map((field: any) => field.name).sort()),
      `candidate ${index + 1} sampleOutput keys do not equal the declared output fields`,
    );
    titles.add(requirement.title.trim().toLowerCase());
  }
  assert(titles.size === 3, 'candidate titles are not distinct');
}

async function main(): Promise<void> {
  const usedPorts = new Set<number>();
  const nextPort = async (): Promise<number> => {
    let port = await getFreePort();
    while (usedPorts.has(port)) port = await getFreePort();
    usedPorts.add(port);
    return port;
  };
  const serverPort = await nextPort();
  const fakeLLMPort = await nextPort();
  const baseUrl = `http://127.0.0.1:${serverPort}`;
  const serverDir = path.resolve(__dirname, '..', 'server');
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const stamp = `${Date.now()}-${process.pid}`;
  const dbPath = path.join(os.tmpdir(), `aegis-chunking-${stamp}.db`);
  let serverProc: ChildProcess | undefined;
  let fakeLLM: Awaited<ReturnType<typeof startFakeLLM>> | undefined;

  console.log(`[chunking] starting hermetic chunking acceptance on ${baseUrl}`);
  await buildServer(serverDir);
  fakeLLM = await startFakeLLM(fakeLLMPort);
  serverProc = startServer(serverDir, dbPath, serverPort, serverEnvironment(fakeLLMPort));

  try {
    await waitForHealth(baseUrl);

    const created = await apiJSON<any>(baseUrl, 'POST', '/api/v1/recordings', { recording: chunkFixtureRecording() });
    const recordingId = created.recording?.id;
    assert(recordingId, `recording creation omitted its id: ${JSON.stringify(created)}`);
    assert(created.recording.snapshotCount === SNAPSHOT_COUNT,
      `recording persisted an unexpected snapshot count: ${created.recording.snapshotCount}`);
    console.log(`[chunking] synthetic recording ${recordingId} persisted with ${SNAPSHOT_COUNT} snapshots`);

    const submitted = await apiJSON<any>(baseUrl, 'POST', `/api/v1/recordings/${encodeURIComponent(recordingId)}/requirement-jobs`);
    const jobId = submitted.job?.id;
    assert(jobId, `candidate job submission omitted its id: ${JSON.stringify(submitted)}`);

    const progress: Array<{ chunkCount: number; completedChunks: number }> = [];
    let job: any;
    const deadline = Date.now() + JOB_TIMEOUT_MS;
    while (Date.now() < deadline) {
      const polled = await apiJSON<any>(baseUrl, 'GET', `/api/v1/requirement-jobs/${encodeURIComponent(jobId)}`);
      job = polled.job;
      if (job?.status === 'completed' || job?.status === 'failed') break;
      if (job?.status === 'running' && (job.chunkCount ?? 0) > 0) {
        progress.push({ chunkCount: job.chunkCount, completedChunks: job.completedChunks ?? 0 });
      }
      await sleep(POLL_INTERVAL_MS);
    }
    assert(job, `requirement job ${jobId} never returned a status`);
    assert(job.status === 'completed',
      `requirement job ended as ${job?.status ?? 'timeout'} (code ${job?.errorCode ?? 'UNKNOWN'}, message ${job?.errorMessage ?? 'none'})`);

    // Job API: PR #49 chunk progress fields reflect the chunked analysis.
    assert(job.chunkCount > 1, `requirement job did not chunk the oversized recording (chunkCount=${job.chunkCount})`);
    assert(job.completedChunks === job.chunkCount,
      `requirement job completed with completedChunks=${job.completedChunks} of chunkCount=${job.chunkCount}`);
    assert(Array.isArray(job.result?.chunkLineage) && job.result.chunkLineage.length === job.chunkCount,
      `requirement job omitted per-chunk lineage: ${JSON.stringify(job.result?.chunkLineage)?.slice(0, 400)}`);
    assertValidCandidates(job.result?.candidates);

    // Progress observations while running must advance monotonically toward
    // the completed total.
    for (let index = 1; index < progress.length; index += 1) {
      assert(progress[index].completedChunks >= progress[index - 1].completedChunks,
        `completedChunks regressed while running: ${JSON.stringify(progress)}`);
    }
    if (progress.length > 0) {
      assert(progress.every((point) => point.chunkCount === job.chunkCount && point.completedChunks <= job.chunkCount),
        `running chunk progress was inconsistent with the completed job: ${JSON.stringify(progress)}`);
      console.log(`[chunking] observed running chunk progress: ${progress.map((point) => `${point.completedChunks}/${point.chunkCount}`).join(' -> ')}`);
    }

    // Fake LLM: ordered chunk analyses with complete snapshot coverage and
    // exactly one final synthesis request.
    const { chunkAnalyses, synthesisRequests, fullRecordingRequests } = fakeLLM.observations;
    assert(chunkAnalyses.length > 1, `fake LLM observed only ${chunkAnalyses.length} chunk analysis request(s)`);
    assert(chunkAnalyses.length === job.chunkCount,
      `fake LLM observed ${chunkAnalyses.length} chunk analyses but the job reported chunkCount=${job.chunkCount}`);
    assert(chunkAnalyses.every((chunk, position) => chunk.index === position + 1 && chunk.total === chunkAnalyses.length),
      `chunk analyses did not arrive in snapshot order: ${JSON.stringify(chunkAnalyses.map((chunk) => chunk.index))}`);
    const covered = chunkAnalyses.flatMap((chunk) => chunk.snapshotIndices);
    assert(JSON.stringify(covered) === JSON.stringify(Array.from({ length: SNAPSHOT_COUNT }, (_, index) => index)),
      `chunk analyses did not cover every snapshot in order: ${JSON.stringify(covered)}`);
    assert(synthesisRequests === 1, `fake LLM observed ${synthesisRequests} synthesis requests instead of exactly one`);
    assert(fullRecordingRequests === 0,
      'a full-recording candidate request was sent even though the recording exceeded the chunking budget');

    console.log(`[chunking] job ${jobId} completed: chunkCount=${job.chunkCount}, completedChunks=${job.completedChunks}, `
      + `${chunkAnalyses.length} ordered chunk analyses, 1 synthesis, 3 schema-valid candidates`);
    console.log('[chunking] hermetic chunking acceptance passed');
  } finally {
    if (serverProc) await killServer(serverProc);
    await closeServer(fakeLLM?.server);
    for (const file of [dbPath, `${dbPath}-wal`, `${dbPath}-shm`, binaryPath]) {
      removeIfExists(file);
    }
  }
}

if (require.main === module) {
  main().catch((error) => {
    console.error('[chunking] hermetic chunking acceptance failed:', error);
    process.exit(1);
  });
}
