import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  HOCKEY_ENTRY_URL,
  HOCKEY_FIELDS,
  HOCKEY_REPLAY_QUERY,
  HOCKEY_REQUIREMENT,
  assertHockeyCanonicalPageOneHref,
  assertHockeyPageURL,
  assertHockeyQuery,
  assertHockeyRequirement,
  assertHockeyRowsEqual,
  assertHockeyRule,
  bindHockeyInput,
  canonicalHockeyRows,
  collectVisibleHockeyNextHrefs,
  collectVisibleHockeyPageOneHrefs,
  collectVisibleHockeyRows,
  hockeyCanonicalPageOneIndex,
  type HockeyRow,
} from './scrape-site-hockey';

const row: HockeyRow = {
  team_name: 'New York Rangers',
  year: 1990,
  wins: 36,
  losses: 31,
  win_percentage: 0.45,
  goals_for: 297,
  goals_against: 265,
  goal_difference: 32,
};

function markup(name = 'New York Rangers', hidden = false): string {
  return `<tr class="team"${hidden ? ' hidden' : ''}>
    <td class="name"> ${name} </td><td class="year">1990</td>
    <td class="wins">36</td><td class="losses">31</td><td class="ot-losses"></td>
    <td class="pct">0.45</td><td class="gf">297</td><td class="ga">265</td><td class="diff">32</td>
  </tr>`;
}

function schema(): Record<string, unknown> {
  return {
    type: 'object',
    properties: Object.fromEntries(HOCKEY_FIELDS.map((field) => [field, {
      type: field === 'team_name' ? 'string' : 'number',
    }])),
    required: [...HOCKEY_FIELDS],
  };
}

function validRule(): Record<string, unknown> {
  return {
    entry: HOCKEY_ENTRY_URL,
    domain: 'www.scrapethissite.com',
    selectors: {
      search: { selector: '#q', visible: true },
      rows: { selector: 'tr.team', visible: true },
      next: { text: 'Next', visible: true },
      pageOne: { text: '1', visible: true },
    },
    steps: [
      { action: 'type', target: { $ref: 'search' }, value: '{{team_query}}', submit: true },
      { action: 'navigate', url: `${HOCKEY_ENTRY_URL}?q={{team_query}}`, waitUntil: 'load' },
      { action: 'click', target: { $ref: 'pageOne' } },
      {
        action: 'loop', type: 'fixedCount', count: 10, steps: [
          {
            action: 'extract', name: 'teams', target: { $ref: 'rows' }, multiple: true,
            fields: Object.fromEntries(HOCKEY_FIELDS.map((field) => [field, {
              type: field === 'team_name' ? 'text' : 'number',
              selector: `.${field}`,
            }])),
          },
          {
            action: 'loop', type: 'forEach', items: '{{extracted.teams}}', as: 'team',
            steps: [{
              action: 'sendResult',
              payload: Object.fromEntries(HOCKEY_FIELDS.map((field) => [field, `{{loopItem.${field}}}`])),
            }],
          },
          {
            action: 'if', condition: { type: 'elementNotExists', target: { $ref: 'next' } },
            then: [{ action: 'break' }], else: [{ action: 'click', target: { $ref: 'next' } }],
          },
        ],
      },
    ],
  };
}

describe('Scrape This Site hockey workflow contract', () => {
  beforeEach(() => {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({ width: 100, height: 20 } as DOMRect);
    document.head.innerHTML = `<base href="${HOCKEY_ENTRY_URL}?q=New+York">`;
    document.body.textContent = '';
  });

  afterEach(() => vi.restoreAllMocks());

  it('collects only complete visible rows in DOM order', () => {
    document.body.innerHTML = `<table><tbody>${markup()}${markup('Hidden Team', true)}${markup('New York Islanders')}</tbody></table>`;
    expect(collectVisibleHockeyRows()).toEqual([
      {
        team_name: 'New York Rangers', year: '1990', wins: '36', losses: '31',
        win_percentage: '0.45', goals_for: '297', goals_against: '265', goal_difference: '32',
      },
      {
        team_name: 'New York Islanders', year: '1990', wins: '36', losses: '31',
        win_percentage: '0.45', goals_for: '297', goals_against: '265', goal_difference: '32',
      },
    ]);
  });

  it('accepts one visible Next and rejects disabled or unrelated links', () => {
    document.body.innerHTML = `<ul class="pagination">
      <li><a href="?page_num=2&q=New+York">Next</a></li>
      <li class="disabled"><a href="?page_num=9&q=New+York">Next</a></li>
      <li><a href="?page_num=3&q=New+York">3</a></li>
    </ul>`;
    expect(collectVisibleHockeyNextHrefs()).toEqual([
      `${HOCKEY_ENTRY_URL}?page_num=2&q=New+York`,
    ]);
  });

  it('selects only the visible enabled canonical page-1 control', () => {
    document.body.innerHTML = `<ul class="pagination">
      <li><a href="?page_num=1&q=New+York">1</a></li>
      <li class="disabled"><a href="?page_num=1&q=Other">1</a></li>
      <li><a href="?page_num=2&q=New+York">2</a></li>
    </ul>`;
    expect(collectVisibleHockeyPageOneHrefs()).toEqual([
      `${HOCKEY_ENTRY_URL}?page_num=1&q=New+York`,
    ]);
    const canonical = `${HOCKEY_ENTRY_URL}?page_num=1&q=New+York`;
    expect(assertHockeyCanonicalPageOneHref([canonical], HOCKEY_REPLAY_QUERY).toString()).toBe(canonical);
    expect(() => assertHockeyCanonicalPageOneHref([], HOCKEY_REPLAY_QUERY)).toThrow(/one visible/);
    expect(() => assertHockeyCanonicalPageOneHref([canonical, canonical], HOCKEY_REPLAY_QUERY)).toThrow(/one visible/);
    expect(() => assertHockeyCanonicalPageOneHref([
      `${HOCKEY_ENTRY_URL}?page_num=2&q=New+York`,
    ], HOCKEY_REPLAY_QUERY)).toThrow(/canonical page 1/);
    expect(hockeyCanonicalPageOneIndex([
      { href: `${HOCKEY_ENTRY_URL}?page_num=2&q=New+York`, text: '2' },
      { href: canonical, text: '\n  1  \n' },
    ], canonical)).toBe(1);
    expect(() => hockeyCanonicalPageOneIndex([], canonical)).toThrow(/must be unique/);
    expect(() => hockeyCanonicalPageOneIndex([
      { href: canonical, text: '1' },
      { href: canonical, text: ' 1 ' },
    ], canonical)).toThrow(/must be unique/);
  });

  it('enforces canonical values, exact order, duplicates, and query membership', () => {
    expect(canonicalHockeyRows([{ ...row }], HOCKEY_REPLAY_QUERY, 'test')).toEqual([row]);
    expect(() => canonicalHockeyRows([{ ...row, year: 1990.5 }], HOCKEY_REPLAY_QUERY, 'test')).toThrow(/integer/);
    expect(() => canonicalHockeyRows([{ ...row, team_name: 'Boston Bruins' }], HOCKEY_REPLAY_QUERY, 'test')).toThrow(/query/);
    expect(() => canonicalHockeyRows([row, row], HOCKEY_REPLAY_QUERY, 'test')).toThrow(/duplicate/);
    expect(() => assertHockeyRowsEqual([row], schema(), [row], HOCKEY_REPLAY_QUERY, 'replay')).not.toThrow();
    expect(() => assertHockeyRowsEqual([], schema(), [row], HOCKEY_REPLAY_QUERY, 'replay')).toThrow(/differ/);
  });

  it('fails closed on unreviewed URL boundaries and parameters', () => {
    expect(assertHockeyPageURL(`${HOCKEY_ENTRY_URL}?q=New+York&page_num=2`, HOCKEY_REPLAY_QUERY).hostname)
      .toBe('www.scrapethissite.com');
    expect(() => assertHockeyPageURL('http://www.scrapethissite.com/pages/forms/?q=New+York', HOCKEY_REPLAY_QUERY)).toThrow(/HTTPS/);
    expect(() => assertHockeyPageURL('https://example.com/pages/forms/?q=New+York', HOCKEY_REPLAY_QUERY)).toThrow(/reviewed host/);
    expect(() => assertHockeyPageURL(`${HOCKEY_ENTRY_URL}?q=New+York&page_num=11`, HOCKEY_REPLAY_QUERY)).toThrow(/within 10/);
    expect(() => assertHockeyPageURL(`${HOCKEY_ENTRY_URL}?q=New+York&redirect=https://evil.invalid`, HOCKEY_REPLAY_QUERY)).toThrow(/unreviewed/);
  });

  it('preserves the structured requirement and exact input binding', () => {
    expect(() => assertHockeyRequirement(HOCKEY_REQUIREMENT)).not.toThrow();
    expect(bindHockeyInput({ team_query: 'placeholder' }, 'Boston Bruins', 'task')).toEqual({ team_query: 'Boston Bruins' });
    expect(() => bindHockeyInput({ extra: 'x', team_query: 'x' }, 'Boston Bruins', 'task')).toThrow(/only team_query/);
    expect(() => assertHockeyQuery('New  York')).toThrow(/safe ASCII/);
  });

  it('requires bound form input, visible extraction, and bounded terminal pagination', () => {
    expect(() => assertHockeyRule(validRule())).not.toThrow();
    const hardcoded = validRule();
    (hardcoded.steps as Record<string, unknown>[])[0].value = HOCKEY_REPLAY_QUERY;
    expect(() => assertHockeyRule(hardcoded)).toThrow(/bind team_query|hardcodes/);
    const constructed = validRule();
    (constructed.steps as Record<string, unknown>[]).push({ action: 'navigate', url: '?page_num=2' });
    expect(() => assertHockeyRule(constructed)).toThrow(/constructed/);
    const missingCanonicalPage = validRule();
    (missingCanonicalPage.steps as Record<string, unknown>[]).splice(2, 1);
    expect(() => assertHockeyRule(missingCanonicalPage)).toThrow(/page-1/);
    const missingQueryCommit = validRule();
    (missingQueryCommit.steps as Record<string, unknown>[]).splice(1, 1);
    expect(() => assertHockeyRule(missingQueryCommit)).toThrow(/committed query navigation/);
    const unboundQueryCommit = validRule();
    (unboundQueryCommit.steps as Record<string, unknown>[])[1] = {
      action: 'navigate', url: `${HOCKEY_ENTRY_URL}?q=New+York`, waitUntil: 'load',
    };
    expect(() => assertHockeyRule(unboundQueryCommit)).toThrow(/hardcodes|committed query navigation/);
  });
});
