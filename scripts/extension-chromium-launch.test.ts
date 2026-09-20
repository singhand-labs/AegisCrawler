import { describe, expect, it } from 'vitest';
import { extensionChromiumArgs } from './extension-chromium-launch';

describe('extension Chromium launch arguments', () => {
  it('loads the MV3 extension in the new headless mode without security bypasses', () => {
    const args = extensionChromiumArgs('C:\\aegis\\dist\\extension', true);

    expect(args).toEqual([
      '--headless=new',
      '--disable-extensions-except=C:/aegis/dist/extension',
      '--load-extension=C:/aegis/dist/extension',
    ]);
    expect(args).not.toContain('--disable-web-security');
    expect(args).not.toContain('--allow-file-access-from-files');
  });

  it('keeps headed launches headed and origin-enforced', () => {
    const args = extensionChromiumArgs('/tmp/aegis/dist/extension', false);

    expect(args).toEqual([
      '--disable-extensions-except=/tmp/aegis/dist/extension',
      '--load-extension=/tmp/aegis/dist/extension',
    ]);
    expect(args.every((arg) => !arg.startsWith('--headless'))).toBe(true);
  });
});
