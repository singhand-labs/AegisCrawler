import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

// Keep this filename outside Vitest's *.test.* glob; Node runs it explicitly.
import {
  IntegrityError,
  classifyMainPush,
  fetchAssociatedPulls,
  identityProblems,
  parseCommitLog,
  readCommitRange,
  verifyCommitIdentities,
  verifyMainPushEvent,
} from './repository-integrity.mjs';

function validEvent(overrides = {}) {
  return {
    after: 'a'.repeat(40),
    before: 'b'.repeat(40),
    created: false,
    deleted: false,
    forced: false,
    ref: 'refs/heads/main',
    repository: { default_branch: 'main' },
    ...overrides,
  };
}

function validPull(overrides = {}) {
  return {
    base: { ref: 'main' },
    commits: 3,
    head: { sha: 'c'.repeat(40) },
    merge_commit_sha: 'a'.repeat(40),
    merged_at: '2026-07-29T00:00:00Z',
    number: 184,
    state: 'closed',
    ...overrides,
  };
}

function validTopology(overrides = {}) {
  return {
    beforeIsAncestor: true,
    firstParentCommitCount: 1,
    headParents: ['b'.repeat(40), 'c'.repeat(40)],
    rangeCommitCount: 4,
    ...overrides,
  };
}

test('accepts real and GitHub-provided identities', () => {
  assert.deepEqual(
    identityProblems({
      name: 'Crystal',
      email: '12345+crystal@users.noreply.github.com',
    }),
    [],
  );
});

test('rejects placeholder names, reserved domains, and malformed emails', () => {
  assert.deepEqual(
    identityProblems({ name: 'Your Name', email: 'you@example.com' }, 'author'),
    [
      'author name is a placeholder',
      'author email uses a reserved placeholder domain',
    ],
  );
  assert.deepEqual(
    identityProblems({ name: 'Crystal', email: 'not-an-email' }, 'committer'),
    ['committer email is malformed'],
  );
});

test('requires exact NUL-delimited commit metadata field arity', () => {
  assert.throws(
    () => parseCommitLog([
      'a'.repeat(40),
      'Alice',
      'alice@singhand.com',
      'Bob',
      'bob@singhand.com',
      'injected',
      '',
    ].join('\0')),
    /malformed commit metadata/,
  );
});

test('checks author and committer identities independently', () => {
  assert.throws(
    () => verifyCommitIdentities([{
      author: { name: 'Crystal', email: 'crystal@singhand.com' },
      committer: { name: 'Your Name', email: 'you@example.com' },
      sha: 'a'.repeat(40),
    }]),
    /committer name is a placeholder/,
  );
});

test('accepts only the exact merged PR outcome on the default branch', () => {
  const pull = classifyMainPush(validEvent(), [
    validPull(),
    validPull({
      merge_commit_sha: 'c'.repeat(40),
      number: 185,
      state: 'open',
      merged_at: null,
    }),
  ], validTopology());
  assert.equal(pull.number, 184);
});

test('accepts squash and rebase merge topologies', () => {
  assert.equal(
    classifyMainPush(
      validEvent(),
      [validPull()],
      validTopology({
        headParents: ['b'.repeat(40)],
        rangeCommitCount: 1,
      }),
    ).number,
    184,
  );
  assert.equal(
    classifyMainPush(
      validEvent(),
      [validPull()],
      validTopology({
        firstParentCommitCount: 3,
        headParents: ['d'.repeat(40)],
        rangeCommitCount: 3,
      }),
    ).number,
    184,
  );
});

for (const [name, event, pulls, topology] of [
  ['direct push', validEvent(), [], validTopology()],
  ['wrong merge SHA', validEvent(), [validPull({ merge_commit_sha: 'c'.repeat(40) })], validTopology()],
  ['open PR', validEvent(), [validPull({ state: 'open', merged_at: null })], validTopology()],
  ['wrong base branch', validEvent(), [validPull({ base: { ref: 'release' } })], validTopology()],
  ['forced push', validEvent({ forced: true }), [validPull()], validTopology()],
  ['created branch', validEvent({ created: true }), [validPull()], validTopology()],
  ['ambiguous PR result', validEvent(), [validPull(), validPull({ number: 185 })], validTopology()],
  ['non-ancestor update', validEvent(), [validPull()], validTopology({ beforeIsAncestor: false })],
  [
    'batched merge topology',
    validEvent(),
    [validPull()],
    validTopology({
      headParents: ['d'.repeat(40), 'c'.repeat(40)],
      rangeCommitCount: 5,
    }),
  ],
  [
    'octopus merge topology',
    validEvent(),
    [validPull()],
    validTopology({
      headParents: ['b'.repeat(40), 'c'.repeat(40), 'd'.repeat(40)],
    }),
  ],
]) {
  test(`rejects ${name}`, () => {
    assert.throws(
      () => classifyMainPush(event, pulls, topology),
      IntegrityError,
    );
  });
}

test('follows GitHub commit-association pagination', async () => {
  const calls = [];
  const pulls = await fetchAssociatedPulls({
    apiUrl: 'https://api.github.test',
    fetchImpl: async (url) => {
      calls.push(url);
      if (calls.length === 1) {
        return new Response(JSON.stringify([{ number: 1 }]), {
          headers: {
            Link: '<https://api.github.test/repos/owner/repository/commits/'
              + `${'a'.repeat(40)}/pulls?per_page=100&page=2>; rel="next"`,
          },
        });
      }
      return new Response(JSON.stringify([{ number: 2 }]));
    },
    repository: 'owner/repository',
    sha: 'a'.repeat(40),
    token: 'test-token',
  });
  assert.deepEqual(pulls.map((pull) => pull.number), [1, 2]);
  assert.equal(calls.length, 2);
});

test('retries until the exact merged PR association is available', async () => {
  let lookupCount = 0;
  const pull = await verifyMainPushEvent({
    attempts: 3,
    event: validEvent(),
    loadAssociatedPulls: async () => {
      lookupCount += 1;
      return lookupCount === 1
        ? [validPull({ merge_commit_sha: 'c'.repeat(40), state: 'open' })]
        : [validPull({ commits: undefined })];
    },
    loadPullDetail: async () => validPull(),
    topology: validTopology(),
    wait: async () => {},
  });
  assert.equal(lookupCount, 2);
  assert.equal(pull.number, 184);
});

test('retries transient GitHub lookup failures', async () => {
  let lookupCount = 0;
  const pull = await verifyMainPushEvent({
    attempts: 2,
    event: validEvent(),
    loadAssociatedPulls: async () => {
      lookupCount += 1;
      if (lookupCount === 1) {
        throw new IntegrityError('temporary lookup failure');
      }
      return [validPull()];
    },
    loadPullDetail: async () => validPull(),
    topology: validTopology(),
    wait: async () => {},
  });
  assert.equal(lookupCount, 2);
  assert.equal(pull.number, 184);
});

test('checks author and committer identities over an exact Git range', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aegis-integrity-'));
  try {
    execFileSync('git', ['init', '--quiet'], { cwd: directory });
    execFileSync('git', ['config', 'user.name', 'Your Name'], { cwd: directory });
    execFileSync('git', ['config', 'user.email', 'you@example.com'], {
      cwd: directory,
    });
    execFileSync('git', ['commit', '--allow-empty', '-m', 'base'], {
      cwd: directory,
    });
    const base = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: directory,
      encoding: 'utf8',
    }).trim();

    execFileSync('git', ['config', 'user.name', 'Crystal'], { cwd: directory });
    execFileSync('git', ['config', 'user.email', 'crystal@singhand.com'], {
      cwd: directory,
    });
    execFileSync('git', ['commit', '--allow-empty', '-m', 'valid'], {
      cwd: directory,
    });
    const validHead = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: directory,
      encoding: 'utf8',
    }).trim();
    assert.equal(
      verifyCommitIdentities(readCommitRange({
        base,
        head: validHead,
        cwd: directory,
      })),
      1,
    );

    execFileSync('git', [
      '-c',
      'user.name=Your Name',
      '-c',
      'user.email=you@example.com',
      'commit',
      '--allow-empty',
      '-m',
      'placeholder',
    ], { cwd: directory });
    const placeholderHead = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: directory,
      encoding: 'utf8',
    }).trim();
    assert.throws(
      () => verifyCommitIdentities(readCommitRange({
        base: validHead,
        head: placeholderHead,
        cwd: directory,
      })),
      /author name is a placeholder/,
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

test('rejects delimiter injection in a real Git identity', () => {
  const directory = mkdtempSync(join(tmpdir(), 'aegis-integrity-injection-'));
  try {
    execFileSync('git', ['init', '--quiet'], { cwd: directory });
    execFileSync('git', ['config', 'user.name', 'Crystal'], { cwd: directory });
    execFileSync('git', ['config', 'user.email', 'crystal@singhand.com'], {
      cwd: directory,
    });
    execFileSync('git', ['commit', '--allow-empty', '-m', 'base'], {
      cwd: directory,
    });
    const base = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: directory,
      encoding: 'utf8',
    }).trim();

    execFileSync('git', ['commit', '--allow-empty', '-m', 'injected'], {
      cwd: directory,
      env: {
        ...process.env,
        GIT_AUTHOR_EMAIL: 'you@example.com',
        GIT_AUTHOR_NAME: 'Alice\x1falice@singhand.com\x1fBob\x1fbob@singhand.com',
        GIT_COMMITTER_EMAIL: 'you@example.com',
        GIT_COMMITTER_NAME: 'Your Name',
      },
    });
    const head = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: directory,
      encoding: 'utf8',
    }).trim();
    assert.throws(
      () => verifyCommitIdentities(readCommitRange({
        base,
        cwd: directory,
        head,
      })),
      /author name contains control characters/,
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});
