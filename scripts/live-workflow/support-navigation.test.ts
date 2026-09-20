import { describe, expect, it } from 'vitest';
import { scenarioNavigationWaitUntil } from './navigation-readiness';

describe('live workflow initial navigation readiness', () => {
  it('preserves full-load readiness for existing scenarios', () => {
    expect(scenarioNavigationWaitUntil({})).toBe('load');
  });

  it('allows an explicitly reviewed scenario to stop at DOM readiness', () => {
    expect(scenarioNavigationWaitUntil({
      navigationWaitUntil: 'domcontentloaded',
    })).toBe('domcontentloaded');
  });
});
