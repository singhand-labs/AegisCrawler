/**
 * Chromium arguments shared by extension-backed manual and qualification runs.
 *
 * Keep this list intentionally narrow: these runs qualify the extension under
 * normal browser origin enforcement, so broad web-security bypasses must never
 * be added here.
 */
export function extensionChromiumArgs(extensionDir: string, headless: boolean): string[] {
  const normalizedExtensionDir = extensionDir.replace(/\\/g, '/');
  return [
    ...(headless ? ['--headless=new'] : []),
    `--disable-extensions-except=${normalizedExtensionDir}`,
    `--load-extension=${normalizedExtensionDir}`,
  ];
}
