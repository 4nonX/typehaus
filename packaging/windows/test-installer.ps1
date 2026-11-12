# Installs, updates and uninstalls typehaus with the setup, silently, and
# checks every trace it leaves. Used by CI:  pwsh packaging/windows/test-installer.ps1 dist\typehaus-setup-windows.exe
param([Parameter(Mandatory)][string]$Setup)
$ErrorActionPreference = 'Stop'

$dir = Join-Path $env:LOCALAPPDATA 'Programs\typehaus'
$uninstKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\typehaus'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$link = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\typehaus.lnk'

function Fail([string]$msg) {
  Write-Host "FAIL: $msg"
  if ($env:GITHUB_ACTIONS -eq 'true') { Write-Host "::error title=Windows installer::$msg" }
  exit 1
}
function Ok([string]$msg) { Write-Host "ok: $msg" }
function RunValue { (Get-ItemProperty -Path $runKey -Name typehaus -ErrorAction SilentlyContinue).typehaus }
function AppProcess { Get-Process -Name typehaus -ErrorAction SilentlyContinue | Select-Object -First 1 }
function WaitFor([scriptblock]$cond, [int]$seconds = 20) {
  for ($i = 0; $i -lt $seconds * 2; $i++) { if (& $cond) { return $true }; Start-Sleep -Milliseconds 500 }
  return $false
}
function Install([string]$what) {
  $p = Start-Process -FilePath $Setup -ArgumentList '/S' -Wait -PassThru
  if ($p.ExitCode -ne 0) { Fail "$what – setup exit code $($p.ExitCode)" }
  Ok "$what – setup finished"
}

# ---- 1. fresh install -------------------------------------------------------
Install 'fresh install'
foreach ($f in 'typehaus.exe', 'typehaus-agent.exe', 'uninstall.exe') {
  if (-not (Test-Path (Join-Path $dir $f))) { Fail "missing $dir\$f" }
}
Ok "program files in $dir"
$entry = Get-ItemProperty -Path $uninstKey -ErrorAction SilentlyContinue
if (-not $entry) { Fail 'no entry under Settings → Apps' }
if ($entry.InstallLocation -ne $dir) { Fail "InstallLocation is '$($entry.InstallLocation)'" }
if (-not $entry.DisplayVersion) { Fail 'no DisplayVersion' }
if ($entry.QuietUninstallString -notlike '*uninstall.exe*/S') { Fail "QuietUninstallString '$($entry.QuietUninstallString)'" }
Ok "Apps entry: $($entry.DisplayName) $($entry.DisplayVersion) by $($entry.Publisher)"
if (-not (Test-Path $link)) { Fail 'no Start menu entry' }
Ok 'Start menu entry'
$run = RunValue
if ($run -notlike "*$dir\typehaus.exe*tray --autostart*") { Fail "autostart is '$run'" }
Ok "starts at login: $run"
$v = & (Join-Path $dir 'typehaus-agent.exe') version
Ok "installed version $v"
if (-not (WaitFor { AppProcess })) { Fail 'silent install did not start typehaus' }
$first = (AppProcess).Id
Ok "typehaus is running (pid $first)"

# ---- 2. update while typehaus is running -----------------------------------
Install 'update over a running app'
if (-not (WaitFor { $p = AppProcess; $p -and $p.Id -ne $first })) { Fail 'the old app was not replaced by the new one' }
if ((Get-Process -Name typehaus -ErrorAction SilentlyContinue | Measure-Object).Count -ne 1) { Fail 'more than one typehaus running after the update' }
Ok "app restarted after the update (pid $((AppProcess).Id))"

# ---- 3. "Start at login" switched off stays off on updates -----------------
Remove-ItemProperty -Path $runKey -Name typehaus
Install 'update with autostart off'
if (RunValue) { Fail 'update switched autostart back on' }
Ok "the user's autostart choice is kept"

# ---- 4. uninstall -------------------------------------------------------------
$p = Start-Process -FilePath (Join-Path $dir 'uninstall.exe') -ArgumentList '/S' -Wait -PassThru
# the NSIS uninstaller continues from a temporary copy: wait for it
if (-not (WaitFor { -not (Test-Path $uninstKey) } 60)) { Fail 'Apps entry still present after uninstall' }
if (-not (WaitFor { -not (Test-Path $dir) } 30)) { Fail "$dir still present after uninstall" }
if (Test-Path $link) { Fail 'Start menu entry still present' }
if (RunValue) { Fail 'autostart entry still present' }
if (-not (WaitFor { -not (AppProcess) })) { Fail 'typehaus still running after uninstall' }
Ok 'uninstalled without leftovers'
Write-Host 'INSTALLER TEST PASSED'
