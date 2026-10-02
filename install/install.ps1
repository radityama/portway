# Review and execute this local script; it never changes PATH or elevates.
param(
  [Parameter(Mandatory=$true)][string]$Version,
  [Parameter(Mandatory=$true)][string]$Directory,
  [string]$AssetsDirectory = '',
  [string]$ChecksumsSha256 = ''
)
$ErrorActionPreference = 'Stop'
if ($Version.Length -gt 64 -or $Version -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$') { throw 'Pin an explicit release version' }
$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($arch) { 'x64' { $arch='amd64' }; 'arm64' { $arch='arm64' }; default { throw 'Unsupported Windows architecture' } }
$asset = "portway_${Version}_windows_${arch}.exe"
$Directory = [System.IO.Path]::GetFullPath($Directory)
if (!(Test-Path -LiteralPath $Directory)) {
  $null = New-Item -ItemType Directory -Path $Directory
  $acl = New-Object System.Security.AccessControl.DirectorySecurity
  $acl.SetAccessRuleProtection($true,$false)
  $sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
  $acl.SetOwner($sid)
  $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($sid,'FullControl','ContainerInherit,ObjectInherit','None','Allow')))
  Set-Acl -LiteralPath $Directory -AclObject $acl
}
$destination = Get-Item -LiteralPath $Directory
if (!$destination.PSIsContainer -or ($destination.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) { throw 'Unsafe destination' }
$directoryAcl = Get-Acl -LiteralPath $Directory
$actorSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$ownerSid = $directoryAcl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value
if ($ownerSid -ne $actorSid) { throw 'Destination must belong to your account' }
$writeRights = [System.Security.AccessControl.FileSystemRights]'Write,Delete,DeleteSubdirectoriesAndFiles,ChangePermissions,TakeOwnership'
foreach ($rule in $directoryAcl.Access) {
  $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
  if ($rule.AccessControlType -eq 'Allow' -and $sid -notin @($actorSid,'S-1-5-18','S-1-5-32-544') -and ($rule.FileSystemRights -band $writeRights)) { throw 'Destination is writable by another account' }
}
$target = Join-Path $Directory 'portway.exe'
if (Test-Path -LiteralPath $target) { $old=Get-Item -LiteralPath $target; if ($old.PSIsContainer -or ($old.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) { throw 'Unsafe existing executable' } }
$stage = Join-Path $Directory ('.portway-install-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $stage
try {
  function Fetch([string]$name,[long]$limit) {
    $path = Join-Path $stage $name
    if ($AssetsDirectory) {
      $source=Get-Item -LiteralPath (Join-Path $AssetsDirectory $name)
      if ($source.PSIsContainer -or ($source.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -or $source.Length -gt $limit) { throw 'Unsafe offline asset' }
      Copy-Item -LiteralPath $source.FullName -Destination $path
    } else {
      # curl.exe ships with supported Windows; restrict both redirects and origin.
      & curl.exe --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 --max-filesize $limit --output $path "https://github.com/radityama/portway/releases/download/$Version/$name"
      if ($LASTEXITCODE -ne 0) { throw 'HTTPS download failed' }
    }
    if ((Get-Item -LiteralPath $path).Length -gt $limit) { throw 'Asset exceeds size bound' }
    return $path
  }
  $sums = Fetch 'SHA256SUMS' 16384
  if ($ChecksumsSha256) { if ($ChecksumsSha256 -cnotmatch '^[0-9a-f]{64}$' -or (Get-FileHash -LiteralPath $sums -Algorithm SHA256).Hash.ToLowerInvariant() -cne $ChecksumsSha256) { throw 'Checksum file does not match trusted digest' } }
  $checksumLines = @(Get-Content -LiteralPath $sums | Where-Object { $_ -cmatch ('^[0-9a-f]{64}  ' + [regex]::Escape($asset) + '$') })
  if ($checksumLines.Count -ne 1) { throw 'Missing or duplicate artifact checksum' }
  $binary = Fetch $asset 104857600
  if ((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant() -cne $checksumLines[0].Substring(0,64)) { throw 'Artifact checksum mismatch' }
  if (Test-Path -LiteralPath $target) {
    $backup = Join-Path $stage 'portway-backup.exe'
    [System.IO.File]::Replace($binary,$target,$backup)
  } else { [System.IO.File]::Move($binary,$target) }
  Write-Output "Installed Portway $Version at $target"
} finally { Remove-Item -LiteralPath $stage -Recurse -Force }
