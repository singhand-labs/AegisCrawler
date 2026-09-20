import { describe, expect, it } from 'vitest';
import {
  assertSearchJourneyActionOrder,
  assertMainstreamUrlAllowed,
  classifyOrganicResults,
  findPublicBoundary,
  isReviewedGithubRepository,
  isWikipediaWebScrapingArticle,
  normalizeVisibleTexts,
  requireMainstreamApproval,
  resolveActiveSuggestionText,
  resolveMainstreamEntryPauseMs,
  resolveMainstreamJourneys,
  resolveSkipSuggestions,
  selectReviewedOrganicResult,
  visibleSuggestionTexts,
} from './mainstream-readonly-qualification';

describe('mainstream read-only qualification gates', () => {
  it('requires the exact fresh one-run approval', () => {
    expect(() => requireMainstreamApproval({})).toThrow(
      'set AEGIS_MAINSTREAM_READONLY_APPROVAL exactly',
    );
    expect(() => requireMainstreamApproval({
      AEGIS_MAINSTREAM_READONLY_APPROVAL: 'yes',
    })).toThrow('set AEGIS_MAINSTREAM_READONLY_APPROVAL exactly');
    expect(() => requireMainstreamApproval({
      AEGIS_MAINSTREAM_READONLY_APPROVAL:
        'I approve one read-only mainstream-site qualification',
    })).not.toThrow();
  });

  it('allows only the reviewed HTTPS hosts', () => {
    expect(() => assertMainstreamUrlAllowed('https://en.wikipedia.org/wiki/Web_scraping'))
      .not.toThrow();
    expect(() => assertMainstreamUrlAllowed('https://github.com/microsoft/vscode'))
      .not.toThrow();
    expect(() => assertMainstreamUrlAllowed('https://www.bing.com/search?q=eclipse'))
      .not.toThrow();
    expect(() => assertMainstreamUrlAllowed('https://duckduckgo.com/?q=eclipse'))
      .not.toThrow();
    expect(() => assertMainstreamUrlAllowed('https://science.nasa.gov/eclipses/'))
      .not.toThrow();
    expect(() => assertMainstreamUrlAllowed('http://en.wikipedia.org/')).toThrow(
      'requires HTTPS',
    );
    expect(() => assertMainstreamUrlAllowed('https://example.com/')).toThrow(
      'blocked unapproved host',
    );
  });

  it('selects only reviewed mainstream journeys', () => {
    expect(resolveMainstreamJourneys({})).toEqual(['wikipedia', 'github', 'bing-search']);
    expect(resolveMainstreamJourneys({ AEGIS_MAINSTREAM_JOURNEYS: 'bing-search' }))
      .toEqual(['bing-search']);
    expect(resolveMainstreamJourneys({
      AEGIS_MAINSTREAM_JOURNEYS: 'bing-search,duckduckgo-search',
    })).toEqual(['bing-search', 'duckduckgo-search']);
    expect(() => resolveMainstreamJourneys({ AEGIS_MAINSTREAM_JOURNEYS: 'bing-search,other' }))
      .toThrow('unknown mainstream journey: other');
  });

  it('keeps the one-minute entry pause explicit and diagnostic-only', () => {
    expect(resolveMainstreamEntryPauseMs({})).toBe(0);
    expect(resolveMainstreamEntryPauseMs({ AEGIS_MAINSTREAM_ENTRY_PAUSE_MS: '60000' }))
      .toBe(60_000);
    expect(() => resolveMainstreamEntryPauseMs({
      AEGIS_MAINSTREAM_ENTRY_PAUSE_MS: '1000',
    })).toThrow('permits only exactly 60000');
  });

  it('requires direct-search mode to be explicitly enabled', () => {
    expect(resolveSkipSuggestions({})).toBe(false);
    expect(resolveSkipSuggestions({ AEGIS_MAINSTREAM_SKIP_SUGGESTIONS: '1' })).toBe(true);
    expect(() => resolveSkipSuggestions({ AEGIS_MAINSTREAM_SKIP_SUGGESTIONS: 'yes' }))
      .toThrow('must be exactly 1');
  });

  it('normalizes and deduplicates visible autocomplete text', () => {
    expect(normalizeVisibleTexts([
      ' how do solar eclipses happen ', 'how  do solar eclipses happen', '',
      'solar eclipse explained',
    ])).toEqual(['how do solar eclipses happen', 'solar eclipse explained']);
  });

  it('binds keyboard browsing to a visible active suggestion identity', () => {
    const options = [
      {
        id: 'suggestion-1', text: 'first suggestion', visible: true,
        ariaSelected: 'false', className: 'sa_sg',
      },
      {
        id: 'suggestion-2', text: ' second   suggestion ', visible: true,
        ariaSelected: 'false', className: 'sa_sg',
      },
    ];
    expect(resolveActiveSuggestionText({
      activeDescendant: 'suggestion-2', options,
    })).toBe('second suggestion');
    expect(resolveActiveSuggestionText({
      activeDescendant: '',
      options: options.map((option, index) => ({
        ...option, className: index === 0 ? 'sa_sg sa_hv' : option.className,
      })),
    })).toBe('first suggestion');
    expect(resolveActiveSuggestionText({
      activeDescendant: 'hidden',
      options: [{
        id: 'hidden', text: 'hidden suggestion', visible: false,
        ariaSelected: 'true', className: 'sa_hv',
      }],
    })).toBeUndefined();
  });

  it('ignores hidden first matches when counting the visible suggestion popup', () => {
    expect(visibleSuggestionTexts({
      activeDescendant: '',
      options: [
        {
          id: 'stale', text: 'stale hidden term', visible: false,
          ariaSelected: '', className: 'sa_sg',
        },
        {
          id: 'one', text: 'solar eclipse facts', visible: true,
          ariaSelected: '', className: 'sa_sg',
        },
        {
          id: 'two', text: 'solar eclipse causes', visible: true,
          ariaSelected: '', className: 'sa_sg',
        },
        {
          id: 'three', text: 'solar eclipse explained', visible: true,
          ariaSelected: '', className: 'sa_sg',
        },
      ],
    })).toEqual([
      'solar eclipse facts', 'solar eclipse causes', 'solar eclipse explained',
    ]);
  });

  it('selects the first exact reviewed HTTPS organic destination', () => {
    expect(selectReviewedOrganicResult([
      { title: 'Advertisement', href: 'https://ads.example/result' },
      { title: 'NASA eclipse guide', href: 'https://science.nasa.gov/eclipses/' },
      { title: 'Later result', href: 'https://en.wikipedia.org/wiki/Solar_eclipse' },
    ])).toEqual({ title: 'NASA eclipse guide', href: 'https://science.nasa.gov/eclipses/' });
    expect(selectReviewedOrganicResult([
      { title: 'HTTP downgrade', href: 'http://science.nasa.gov/eclipses/' },
      { title: 'Lookalike', href: 'https://science.nasa.gov.example/eclipses/' },
    ])).toBeUndefined();
  });

  it('recognizes alternate organic result shapes while excluding ads and duplicates', () => {
    expect(classifyOrganicResults([
      {
        title: ' First result ', href: 'https://example.org/one#section',
        visible: true, excluded: false,
      },
      {
        title: 'First result', href: 'https://example.org/one',
        visible: true, excluded: false,
      },
      {
        title: 'Second result', href: 'https://example.org/two',
        visible: true, excluded: false,
      },
      {
        title: 'Sponsored result', href: 'https://example.org/ad',
        visible: true, excluded: true,
      },
      {
        title: 'Hidden result', href: 'https://example.org/hidden',
        visible: false, excluded: false,
      },
      {
        title: 'Downgraded result', href: 'http://example.org/http',
        visible: true, excluded: false,
      },
    ])).toEqual([
      { title: 'First result', href: 'https://example.org/one' },
      { title: 'Second result', href: 'https://example.org/two' },
    ]);
  });

  it('requires the complete human search, browse, read, and Back order', () => {
    const complete = [
      'focus-search', 'type-query',
      'browse-suggestion', 'browse-suggestion', 'browse-suggestion',
      'select-suggestion', 'results-ready',
      'scroll-results', 'scroll-results', 'scroll-results',
      'open-result', 'destination-ready',
      'scroll-destination', 'scroll-destination',
      'go-back', 'results-restored',
    ] as const;
    expect(() => assertSearchJourneyActionOrder(complete)).not.toThrow();
    expect(() => assertSearchJourneyActionOrder(complete.slice(0, -1))).toThrow(
      'actions are incomplete or out of order',
    );
  });

  it('requires the complete direct-query result-browsing order', () => {
    expect(() => assertSearchJourneyActionOrder([
      'focus-search', 'type-query', 'submit-query', 'results-ready',
      'scroll-results', 'scroll-results', 'scroll-results',
      'open-result', 'destination-ready',
      'scroll-destination', 'scroll-destination', 'go-back', 'results-restored',
    ], true)).not.toThrow();
  });

  it('detects actual boundaries without matching ordinary discussion', () => {
    expect(findPublicBoundary(
      'The article discusses CAPTCHA design and rate limits.',
      'Web scraping',
    )).toBeUndefined();
    expect(findPublicBoundary(
      'Please verify you are human before continuing.',
      'Security check',
    )).toBe('verify you are human');
  });

  it('accepts Wikipedia direct search-to-article redirects only for the reviewed article', () => {
    expect(isWikipediaWebScrapingArticle(
      'https://en.wikipedia.org/wiki/Web_scraping',
    )).toBe(true);
    expect(isWikipediaWebScrapingArticle(
      'https://en.wikipedia.org/wiki/Web%20scraping',
    )).toBe(true);
    expect(isWikipediaWebScrapingArticle(
      'https://en.wikipedia.org/wiki/Robots.txt',
    )).toBe(false);
  });

  it('binds the GitHub oracle to the exact reviewed repository identity', () => {
    expect(isReviewedGithubRepository({
      full_name: 'microsoft/vscode',
      html_url: 'https://github.com/microsoft/vscode',
    })).toBe(true);
    expect(isReviewedGithubRepository({
      full_name: 'other/AegisCrawler',
      html_url: 'https://github.com/other/AegisCrawler',
    })).toBe(false);
  });

});
