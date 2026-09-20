import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';

const validatedIdentities = new Set<string>();
let windowsIdentityName: string | undefined;

const WINDOWS_ACL_SCRIPT = String.raw`
$ErrorActionPreference = 'Stop'
$target = $env:AEGIS_WINDOWS_PRIVATE_PATH
$kind = $env:AEGIS_WINDOWS_PRIVATE_KIND
$action = $env:AEGIS_WINDOWS_PRIVATE_ACTION
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User

if ($action -eq 'secure') {
  $acl = Get-Acl -LiteralPath $target
  $acl.SetOwner($sid)
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($rule in @($acl.Access)) {
    [void]$acl.RemoveAccessRuleSpecific($rule)
  }
  $inheritance = if ($kind -eq 'directory') {
    [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
      [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
  } else {
    [System.Security.AccessControl.InheritanceFlags]::None
  }
  $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
    $sid,
    [System.Security.AccessControl.FileSystemRights]::FullControl,
    $inheritance,
    [System.Security.AccessControl.PropagationFlags]::None,
    [System.Security.AccessControl.AccessControlType]::Allow
  )
  [void]$acl.AddAccessRule($rule)
  Set-Acl -LiteralPath $target -AclObject $acl
}

$verified = Get-Acl -LiteralPath $target
$owner = ([System.Security.Principal.NTAccount]$verified.Owner).Translate(
  [System.Security.Principal.SecurityIdentifier]
)
$rules = @($verified.GetAccessRules(
  $true,
  $true,
  [System.Security.Principal.SecurityIdentifier]
))
$validRule = $rules.Count -eq 1 -and
  $rules[0].IdentityReference.Value -eq $sid.Value -and
  $rules[0].AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and
  (($rules[0].FileSystemRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -eq
    [System.Security.AccessControl.FileSystemRights]::FullControl) -and
  -not $rules[0].IsInherited
if (-not $verified.AreAccessRulesProtected -or $owner.Value -ne $sid.Value -or -not $validRule) {
  throw 'path must be owned by the current user with a protected private DACL'
}
`;

function runWindowsACL(
  target: string,
  kind: 'directory' | 'file',
  action: 'secure' | 'assert',
): void {
  if (process.platform !== 'win32') return;
  const identity = (): string => {
    const stat = fs.lstatSync(target, { bigint: true });
    return `${stat.dev}:${stat.ino}:${stat.ctimeNs}:${stat.size}:${kind}`;
  };
  if (action === 'assert' && validatedIdentities.has(identity())) return;
  try {
    if (action === 'secure') {
      windowsIdentityName ??= execFileSync('whoami.exe', [], {
        encoding: 'utf8',
        windowsHide: true,
      }).trim();
      const grant = kind === 'directory'
        ? `${windowsIdentityName}:(OI)(CI)F`
        : `${windowsIdentityName}:F`;
      const aclTarget = target.startsWith('\\\\?\\') ? target : `\\\\?\\${target}`;
      execFileSync('icacls.exe', [aclTarget, '/inheritance:r', '/grant:r', grant, '/Q'], {
        stdio: 'pipe',
        windowsHide: true,
      });
      execFileSync('icacls.exe', [aclTarget, '/setowner', windowsIdentityName, '/Q'], {
        stdio: 'pipe',
        windowsHide: true,
      });
      validatedIdentities.add(identity());
      return;
    }
    execFileSync('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', WINDOWS_ACL_SCRIPT], {
      env: {
        ...process.env,
        AEGIS_WINDOWS_PRIVATE_ACTION: action,
        AEGIS_WINDOWS_PRIVATE_KIND: kind,
        AEGIS_WINDOWS_PRIVATE_PATH: target,
      },
      stdio: 'pipe',
      windowsHide: true,
    });
    validatedIdentities.add(identity());
  } catch {
    throw new Error(`${kind} must be owned by the current user with a protected private Windows DACL`);
  }
}

export function secureWindowsPrivatePath(target: string, kind: 'directory' | 'file'): void {
  runWindowsACL(target, kind, 'secure');
}

export function assertWindowsPrivatePath(target: string, kind: 'directory' | 'file'): void {
  runWindowsACL(target, kind, 'assert');
}
