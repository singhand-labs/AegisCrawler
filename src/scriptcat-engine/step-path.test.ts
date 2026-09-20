/// <reference types="vitest/globals" />
import {
  EMPTY_PATH,
  nextPendingPath,
  prevPendingPath,
  comparePath,
  formatPath,
  type StepPath,
} from './step-path';

// ---------- nextPendingPath -------------------------------------------------

describe('nextPendingPath', () => {
  it('returns the first top-level step from an empty path', () => {
    expect(nextPendingPath(EMPTY_PATH)).toEqual([{ kind: 'top', childIdx: 0 }]);
  });

  it('increments a bare top-level childIdx', () => {
    expect(nextPendingPath([{ kind: 'top', childIdx: 0 }])).toEqual([
      { kind: 'top', childIdx: 1 },
    ]);
  });

  it('increments a top-level childIdx at arbitrary position', () => {
    expect(nextPendingPath([{ kind: 'top', childIdx: 5 }])).toEqual([
      { kind: 'top', childIdx: 6 },
    ]);
  });

  it('increments the if.then branch childIdx', () => {
    expect(
      nextPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'if', branch: 'then', childIdx: 0 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'if', branch: 'then', childIdx: 1 },
    ]);
  });

  it('increments the if.else branch childIdx', () => {
    expect(
      nextPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'if', branch: 'else', childIdx: 3 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'if', branch: 'else', childIdx: 4 },
    ]);
  });

  it('increments a loop iter childIdx', () => {
    expect(
      nextPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'loop', iter: 0, childIdx: 2 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 0, childIdx: 3 },
    ]);
  });

  it('increments a switch case childIdx', () => {
    expect(
      nextPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'switch', caseIdx: 1, childIdx: 0 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'switch', caseIdx: 1, childIdx: 1 },
    ]);
  });

  it('increments a group childIdx', () => {
    expect(
      nextPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'group', childIdx: 7 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'group', childIdx: 8 },
    ]);
  });

  it('increments the deepest frame of a 3-level path', () => {
    const deep: StepPath = [
      { kind: 'top', childIdx: 0 },
      { kind: 'if', branch: 'then', childIdx: 0 },
      { kind: 'loop', iter: 2, childIdx: 1 },
    ];
    expect(nextPendingPath(deep)).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'if', branch: 'then', childIdx: 0 },
      { kind: 'loop', iter: 2, childIdx: 2 },
    ]);
  });

  it('does not mutate the input', () => {
    const input: StepPath = [{ kind: 'top', childIdx: 3 }];
    const snapshot = JSON.parse(JSON.stringify(input));
    nextPendingPath(input);
    expect(input).toEqual(snapshot);
  });
});

// ---------- prevPendingPath -------------------------------------------------

describe('prevPendingPath', () => {
  it('returns EMPTY_PATH for EMPTY_PATH (no-op)', () => {
    expect(prevPendingPath(EMPTY_PATH)).toEqual([]);
  });

  it('decrements a top-level childIdx', () => {
    expect(prevPendingPath([{ kind: 'top', childIdx: 5 }])).toEqual([
      { kind: 'top', childIdx: 4 },
    ]);
  });

  it('allows decrementing below zero (container re-entry sentinel)', () => {
    expect(prevPendingPath([{ kind: 'top', childIdx: 0 }])).toEqual([
      { kind: 'top', childIdx: -1 },
    ]);
  });

  it('decrements an if.then first child to -1', () => {
    expect(
      prevPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'if', branch: 'then', childIdx: 0 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'if', branch: 'then', childIdx: -1 },
    ]);
  });

  it('decrements a loop first-child to childIdx -1 (iter preserved)', () => {
    expect(
      prevPendingPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'loop', iter: 3, childIdx: 0 },
      ]),
    ).toEqual([
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 3, childIdx: -1 },
    ]);
  });

  it('preserves all prefix frames untouched', () => {
    const out = prevPendingPath([
      { kind: 'top', childIdx: 4 },
      { kind: 'group', childIdx: 2 },
    ]);
    expect(out[0]).toEqual({ kind: 'top', childIdx: 4 });
    expect(out[1]).toEqual({ kind: 'group', childIdx: 1 });
  });

  it('does not mutate the input', () => {
    const input: StepPath = [{ kind: 'top', childIdx: 3 }];
    const snapshot = JSON.parse(JSON.stringify(input));
    prevPendingPath(input);
    expect(input).toEqual(snapshot);
  });
});

// ---------- comparePath -----------------------------------------------------

describe('comparePath', () => {
  it('returns 0 for identical paths', () => {
    const p: StepPath = [{ kind: 'top', childIdx: 2 }];
    expect(comparePath(p, p)).toBe(0);
  });

  it('returns 0 for deeply equal paths', () => {
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 1, childIdx: 2 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 1, childIdx: 2 }],
      ),
    ).toBe(0);
  });

  it('treats EMPTY_PATH as less than any non-empty path', () => {
    expect(comparePath([], [{ kind: 'top', childIdx: 0 }])).toBe(-1);
    expect(comparePath([{ kind: 'top', childIdx: 0 }], [])).toBe(1);
  });

  it('orders top-level frames by childIdx', () => {
    expect(comparePath([{ kind: 'top', childIdx: 0 }], [{ kind: 'top', childIdx: 1 }])).toBe(-1);
    expect(comparePath([{ kind: 'top', childIdx: 1 }], [{ kind: 'top', childIdx: 0 }])).toBe(1);
  });

  it('orders a parent frame before its descendants (DFS order)', () => {
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'then', childIdx: 0 }],
      ),
    ).toBe(-1);
  });

  it('orders last descendant of a container before next top-level sibling', () => {
    // [{top,0},{if,then,99}] is the last descendant of top[0] → still less than [{top,1}]
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'then', childIdx: 99 }],
        [{ kind: 'top', childIdx: 1 }],
      ),
    ).toBe(-1);
  });

  it('orders if.then before if.else at same childIdx', () => {
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'then', childIdx: 1 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'else', childIdx: 1 }],
      ),
    ).toBe(-1);
  });

  it('orders loop iterations ascending', () => {
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 1, childIdx: 0 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 2, childIdx: 0 }],
      ),
    ).toBe(-1);
  });

  it('orders switch cases ascending', () => {
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'switch', caseIdx: 0, childIdx: 0 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'switch', caseIdx: 1, childIdx: 0 }],
      ),
    ).toBe(-1);
  });

  it('orders frame kinds by rank when diverging at the same depth', () => {
    // if(rank 1) < loop(rank 2) < switch(rank 3) < group(rank 4)
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'if', branch: 'then', childIdx: 0 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'loop', iter: 0, childIdx: 0 }],
      ),
    ).toBe(-1);
    expect(
      comparePath(
        [{ kind: 'top', childIdx: 0 }, { kind: 'switch', caseIdx: 0, childIdx: 0 }],
        [{ kind: 'top', childIdx: 0 }, { kind: 'group', childIdx: 0 }],
      ),
    ).toBe(-1);
  });

  it('is antisymmetric', () => {
    const a: StepPath = [{ kind: 'top', childIdx: 0 }];
    const b: StepPath = [{ kind: 'top', childIdx: 1 }];
    expect(comparePath(a, b)).toBe(-1);
    expect(comparePath(b, a)).toBe(1);
  });

  it('orders deep paths by deepest divergence', () => {
    const a: StepPath = [
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 5, childIdx: 2 },
      { kind: 'group', childIdx: 1 },
    ];
    const b: StepPath = [
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 5, childIdx: 2 },
      { kind: 'group', childIdx: 9 },
    ];
    expect(comparePath(a, b)).toBe(-1);
  });

  it('detects monotonicity violation (stale background ctx)', () => {
    // sessionStorage has advanced to iter 3, background ctx still at iter 1 → stale
    const ss: StepPath = [
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 3, childIdx: 0 },
    ];
    const bg: StepPath = [
      { kind: 'top', childIdx: 0 },
      { kind: 'loop', iter: 1, childIdx: 2 },
    ];
    expect(comparePath(bg, ss)).toBe(-1);
  });
});

// ---------- formatPath ------------------------------------------------------

describe('formatPath', () => {
  it('renders EMPTY_PATH as <empty>', () => {
    expect(formatPath([])).toBe('<empty>');
  });

  it('renders a single top frame', () => {
    expect(formatPath([{ kind: 'top', childIdx: 2 }])).toBe('top[2]');
  });

  it('renders an if branch frame', () => {
    expect(formatPath([{ kind: 'if', branch: 'else', childIdx: 1 }])).toBe('if.else[1]');
  });

  it('renders a loop frame with iter', () => {
    expect(formatPath([{ kind: 'loop', iter: 4, childIdx: 2 }])).toBe('loop{iter=4}[2]');
  });

  it('renders a switch frame with case', () => {
    expect(formatPath([{ kind: 'switch', caseIdx: 1, childIdx: 0 }])).toBe('switch{case=1}[0]');
  });

  it('renders a group frame', () => {
    expect(formatPath([{ kind: 'group', childIdx: 3 }])).toBe('group[3]');
  });

  it('joins a multi-frame path with /', () => {
    expect(
      formatPath([
        { kind: 'top', childIdx: 0 },
        { kind: 'if', branch: 'then', childIdx: 1 },
        { kind: 'loop', iter: 2, childIdx: 0 },
      ]),
    ).toBe('top[0]/if.then[1]/loop{iter=2}[0]');
  });

  it('renders negative childIdx (rollback sentinel)', () => {
    expect(formatPath([{ kind: 'top', childIdx: -1 }])).toBe('top[-1]');
  });
});
