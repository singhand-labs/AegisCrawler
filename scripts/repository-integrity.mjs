import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const ZERO_SHA_PATTERN = /^0+$/;
const SHA_PATTERN = /^[0-9a-f]{40,64}$/i;
const EMAIL_PATTERN = /^[^\s<>@]+@[^\s<>@]+\.[^\s<>@]+$/;
const CONTROL_CHARACTER_PATTERN = /[\u0000-\u001f\u007f]/;
const PLACEHOLDER_NAMES = new Set([
  'username',
  'your name',
]);
const RESERVED_EMAIL_DOMAINS = new Set([
  'example.com',
  'example.net',
  'example.org',
]);

export class IntegrityError extends Error {
  constructor(message) {
    super(message);
    this.name = 'IntegrityError';
  }
}

function normalize(value) {
  return String(value ?? '').trim().toLowerCase();
}

export function identityProblems({ name, email }, role = 'identity') {
  const problems = [];
  const rawName = String(name ?? '');
  const rawEmail = String(email ?? '');
  const normalizedName = normalize(name);
  const normalizedEmail = normalize(email);
  const emailDomain = normalizedEmail.split('@').at(-1);

  if (CONTROL_CHARACTER_PATTERN.test(rawName)) {
    problems.push(`${role} name contains control characters`);
  } else if (!normalizedName) {
    problems.push(`${role} name is empty`);
  } else if (PLACEHOLDER_NAMES.has(normalizedName)) {
    problems.push(`${role} name is a placeholder`);
  }

  if (CONTROL_CHARACTER_PATTERN.test(rawEmail)) {
    problems.push(`${role} email contains control characters`);
  } else if (!EMAIL_PATTERN.test(normalizedEmail)) {
    problems.push(`${role} email is malformed`);
  } else if (
    RESERVED_EMAIL_DOMAINS.has(emailDomain)
    || emailDomain?.endsWith('.invalid')
    || emailDomain === 'localhost'
  ) {
    problems.push(`${role} email uses a reserved placeholder domain`);
  }

  return problems;
}

export function parseCommitLog(rawLog) {
  const fields = rawLog.split('\0');
  if (fields.at(-1) === '') {
    fields.pop();
  }
  if (fields.length % 5 !== 0) {
    throw new IntegrityError('git log returned malformed commit metadata');
  }

  const commits = [];
  for (let index = 0; index < fields.length; index += 5) {
    const [sha, authorName, authorEmail, committerName, committerEmail] =
      fields.slice(index, index + 5);
    if (!sha) {
      throw new IntegrityError('git log returned an empty commit SHA');
    }
    commits.push({
      sha,
      author: { name: authorName, email: authorEmail },
      committer: { name: committerName, email: committerEmail },
    });
  }
  return commits;
}

function assertSha(value, label) {
  if (!SHA_PATTERN.test(value) || ZERO_SHA_PATTERN.test(value)) {
    throw new IntegrityError(`${label} must be a non-zero Git object SHA`);
  }
}

function git(args, cwd) {
  try {
    return execFileSync('git', args, {
      cwd,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch (error) {
    const detail = String(error.stderr ?? error.message).trim();
    throw new IntegrityError(`git ${args[0]} failed: ${detail}`);
  }
}

function assertCommitExists(sha, cwd) {
  git(['rev-parse', '--verify', `${sha}^{commit}`], cwd);
}

export function readCommitRange({ base, head, cwd = process.cwd() }) {
  assertSha(base, 'base');
  assertSha(head, 'head');
  assertCommitExists(base, cwd);
  assertCommitExists(head, cwd);
  return parseCommitLog(
    git([
      'log',
      '-z',
      '--format=%H%x00%an%x00%ae%x00%cn%x00%ce',
      `${base}..${head}`,
    ], cwd),
  );
}

export function readCommit({ sha, cwd = process.cwd() }) {
  assertSha(sha, 'commit');
  assertCommitExists(sha, cwd);
  return parseCommitLog(
    git([
      'show',
      '-z',
      '--quiet',
      '--format=%H%x00%an%x00%ae%x00%cn%x00%ce',
      sha,
    ], cwd),
  );
}

function isAncestor(base, head, cwd) {
  try {
    execFileSync('git', ['merge-base', '--is-ancestor', base, head], {
      cwd,
      stdio: 'ignore',
    });
    return true;
  } catch (error) {
    if (error.status === 1) {
      return false;
    }
    throw new IntegrityError(`git merge-base failed with status ${error.status}`);
  }
}

export function readPushTopology({
  before,
  after,
  cwd = process.cwd(),
}) {
  assertSha(before, 'push before');
  assertSha(after, 'push after');
  assertCommitExists(before, cwd);
  assertCommitExists(after, cwd);

  const headParents = git([
    'show',
    '--quiet',
    '--format=%P',
    after,
  ], cwd).trim().split(/\s+/).filter(Boolean);

  return {
    beforeIsAncestor: isAncestor(before, after, cwd),
    firstParentCommitCount: Number(git([
      'rev-list',
      '--first-parent',
      '--count',
      `${before}..${after}`,
    ], cwd).trim()),
    headParents,
    rangeCommitCount: Number(git([
      'rev-list',
      '--count',
      `${before}..${after}`,
    ], cwd).trim()),
  };
}

export function verifyCommitIdentities(commits) {
  if (commits.length === 0) {
    throw new IntegrityError('reviewed commit set is empty');
  }

  const failures = [];
  for (const commit of commits) {
    for (const problem of identityProblems(commit.author, 'author')) {
      failures.push(`${commit.sha}: ${problem}`);
    }
    for (const problem of identityProblems(commit.committer, 'committer')) {
      failures.push(`${commit.sha}: ${problem}`);
    }
  }

  if (failures.length > 0) {
    throw new IntegrityError(
      `commit metadata policy failed:\n${failures.map((item) => `- ${item}`).join('\n')}`,
    );
  }

  return commits.length;
}

export function classifyMainPush(event, associatedPulls, topology) {
  const defaultBranch = event.repository?.default_branch;
  const expectedRef = defaultBranch && `refs/heads/${defaultBranch}`;

  if (!defaultBranch || event.ref !== expectedRef) {
    throw new IntegrityError('push event is not for the repository default branch');
  }
  if (event.created || event.deleted || ZERO_SHA_PATTERN.test(event.after ?? '')) {
    throw new IntegrityError('default-branch creation or deletion is not a valid PR merge');
  }
  if (event.forced) {
    throw new IntegrityError('forced default-branch pushes are not permitted');
  }
  assertSha(event.before, 'push before');
  assertSha(event.after, 'push after');
  if (!topology?.beforeIsAncestor) {
    throw new IntegrityError('push before is not an ancestor of push after');
  }

  const exactMergedPulls = associatedPulls.filter((pull) => (
    pull.state === 'closed'
    && Boolean(pull.merged_at)
    && pull.base?.ref === defaultBranch
    && pull.merge_commit_sha === event.after
  ));

  if (exactMergedPulls.length !== 1) {
    throw new IntegrityError(
      'push head is not the exact outcome of one merged pull request into the default branch',
    );
  }

  const pull = exactMergedPulls[0];
  if (!Number.isInteger(pull.commits) || pull.commits < 1) {
    throw new IntegrityError('merged pull request commit count is unavailable');
  }

  const isMergeCommit = (
    topology.headParents.length === 2
    && topology.headParents[0] === event.before
    && topology.headParents[1] === pull.head?.sha
    && topology.rangeCommitCount === pull.commits + 1
  );
  const isSquashCommit = (
    topology.headParents.length === 1
    && topology.headParents[0] === event.before
    && topology.rangeCommitCount === 1
  );
  const isRebaseResult = (
    topology.headParents.length === 1
    && topology.rangeCommitCount === pull.commits
    && topology.firstParentCommitCount === pull.commits
  );

  if (!isMergeCommit && !isSquashCommit && !isRebaseResult) {
    throw new IntegrityError(
      'push range topology does not match a merge, squash, or rebase result for the associated pull request',
    );
  }

  return pull;
}

function parseOptions(args) {
  const options = new Map();
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith('--') || value === undefined) {
      throw new IntegrityError(`invalid option sequence near ${key ?? '<end>'}`);
    }
    options.set(key.slice(2), value);
  }
  return options;
}

function requiredOption(options, name) {
  const value = options.get(name);
  if (!value) {
    throw new IntegrityError(`missing required --${name} option`);
  }
  return value;
}

async function fetchGitHubJson(url, token, fetchImpl = fetch) {
  if (!token) {
    throw new IntegrityError('GITHUB_TOKEN is required for main-push verification');
  }

  try {
    const response = await fetchImpl(url, {
      headers: {
        Accept: 'application/vnd.github+json',
        Authorization: `Bearer ${token}`,
        'X-GitHub-Api-Version': '2022-11-28',
      },
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) {
      throw new IntegrityError(
        `GitHub lookup failed with HTTP ${response.status}`,
      );
    }

    return {
      data: await response.json(),
      link: response.headers.get('link'),
    };
  } catch (error) {
    if (error instanceof IntegrityError) {
      throw error;
    }
    throw new IntegrityError(`GitHub lookup failed: ${error.name ?? 'network error'}`);
  }
}

function repositoryApiPath(apiUrl, repository) {
  if (!/^[^/]+\/[^/]+$/.test(repository ?? '')) {
    throw new IntegrityError('GITHUB_REPOSITORY must be an owner/repository pair');
  }
  const [owner, name] = repository.split('/').map(encodeURIComponent);
  return `${apiUrl.replace(/\/+$/, '')}/repos/${owner}/${name}`;
}

function nextPageUrl(link) {
  if (!link) {
    return null;
  }
  for (const part of link.split(',')) {
    const match = part.match(/<([^>]+)>\s*;\s*rel="next"/);
    if (match) {
      return match[1];
    }
  }
  return null;
}

export async function fetchAssociatedPulls({
  apiUrl,
  repository,
  sha,
  token,
  fetchImpl = fetch,
}) {
  const baseUrl = repositoryApiPath(apiUrl, repository);
  const apiOrigin = new URL(apiUrl).origin;
  const pulls = [];
  let url = `${baseUrl}/commits/${sha}/pulls?per_page=100`;

  for (let page = 1; page <= 10; page += 1) {
    const response = await fetchGitHubJson(url, token, fetchImpl);
    if (!Array.isArray(response.data)) {
      throw new IntegrityError('GitHub commit-to-PR lookup returned a non-array payload');
    }
    pulls.push(...response.data);

    const next = nextPageUrl(response.link);
    if (!next) {
      return pulls;
    }
    if (new URL(next).origin !== apiOrigin) {
      throw new IntegrityError('GitHub pagination attempted to leave the API origin');
    }
    url = next;
  }

  throw new IntegrityError('GitHub commit-to-PR lookup exceeded 10 pages');
}

async function fetchPullDetail({
  apiUrl,
  repository,
  number,
  token,
  fetchImpl = fetch,
}) {
  const baseUrl = repositoryApiPath(apiUrl, repository);
  const response = await fetchGitHubJson(
    `${baseUrl}/pulls/${number}`,
    token,
    fetchImpl,
  );
  const pull = response.data;
  if (!pull || typeof pull !== 'object') {
    throw new IntegrityError('GitHub pull-request lookup returned a non-object payload');
  }
  return pull;
}

export async function verifyMainPushEvent({
  event,
  topology,
  loadAssociatedPulls,
  loadPullDetail,
  attempts = 3,
  wait = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds)),
}) {
  let lastError;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      const pulls = await loadAssociatedPulls();
      const candidates = pulls.filter((pull) => (
        pull.state === 'closed'
        && Boolean(pull.merged_at)
        && pull.base?.ref === event.repository?.default_branch
        && pull.merge_commit_sha === event.after
      ));
      let enrichedPulls = pulls;
      if (candidates.length === 1) {
        const detail = await loadPullDetail(candidates[0].number);
        enrichedPulls = pulls.map((pull) => (
          pull.number === detail.number
            ? { ...pull, commits: detail.commits, head: detail.head }
            : pull
        ));
      }

      const pull = classifyMainPush(event, enrichedPulls, topology);
      return pull;
    } catch (error) {
      lastError = error;
    }

    if (attempt < attempts) {
      await wait(attempt * 2_000);
    }
  }

  throw lastError;
}

async function verifyMainPush(eventPath) {
  const event = JSON.parse(readFileSync(eventPath, 'utf8'));
  const apiUrl = process.env.GITHUB_API_URL ?? 'https://api.github.com';
  const repository = process.env.GITHUB_REPOSITORY;
  const token = process.env.GITHUB_TOKEN;
  const topology = readPushTopology({
    before: event.before,
    after: event.after,
  });
  const pull = await verifyMainPushEvent({
    event,
    topology,
    loadAssociatedPulls: () => fetchAssociatedPulls({
      apiUrl,
      repository,
      sha: event.after,
      token,
    }),
    loadPullDetail: (number) => fetchPullDetail({
      apiUrl,
      repository,
      number,
      token,
    }),
  });
  return `main push is consistent with merged PR #${pull.number}`;
}

export async function runCli(argv) {
  const [command, ...optionArgs] = argv;
  const options = parseOptions(optionArgs);

  if (command === 'commit-range') {
    const count = verifyCommitIdentities(readCommitRange({
      base: requiredOption(options, 'base'),
      head: requiredOption(options, 'head'),
    }));
    return `validated ${count} commit(s) in the reviewed range`;
  }

  if (command === 'commit') {
    const count = verifyCommitIdentities(readCommit({
      sha: requiredOption(options, 'sha'),
    }));
    return `validated ${count} commit`;
  }

  if (command === 'main-push') {
    return verifyMainPush(requiredOption(options, 'event'));
  }

  throw new IntegrityError(`unknown command ${command ?? '<missing>'}`);
}

const isMain = process.argv[1]
  && import.meta.url === pathToFileURL(process.argv[1]).href;

if (isMain) {
  runCli(process.argv.slice(2))
    .then((message) => {
      process.stdout.write(`${message}\n`);
    })
    .catch((error) => {
      process.stderr.write(`${error.message}\n`);
      process.exitCode = 1;
    });
}
