export type NavigationWaitUntil = 'load' | 'domcontentloaded';

export function scenarioNavigationWaitUntil(
  scenario: { navigationWaitUntil?: NavigationWaitUntil },
): NavigationWaitUntil {
  return scenario.navigationWaitUntil ?? 'load';
}
