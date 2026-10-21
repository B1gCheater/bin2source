#Requires -Version 7.0
<#
.SYNOPSIS
    Build bin2source, optionally for another platform.

.DESCRIPTION
    Without arguments this produces the host binary in bin/. Cross compilation
    needs nothing extra: the tool is pure Go with no cgo, so CGO_ENABLED is
    turned off and GOOS/GOARCH are all it takes.

    The Windows twin of ./build.sh, and it names the output the same way.

.EXAMPLE
    ./build.ps1
    -> bin/bin2source.exe

.EXAMPLE
    ./build.ps1 linux amd64
    -> bin/bin2source_linux_amd64

.EXAMPLE
    ./build.ps1 darwin arm64
    -> bin/bin2source_darwin_arm64

.NOTES
    An empty target falls back to the host, so "./build.ps1 '' arm64" behaves
    like the shell script does: GOOS stays the host and GOARCH becomes arm64.
    The arguments are collected as a list because PowerShell drops an empty
    string passed positionally, which would otherwise shift arm64 into GOOS.
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0, ValueFromRemainingArguments)]
    [string[]]$Target
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Default to the host, exactly as ${1:-$(go env GOOS)} does in build.sh.
# Indexing rather than using .Count: an empty $Target is $null, not an empty
# array, and asking $null for its Count is an error under Set-StrictMode.
$TargetOS = ''
$TargetArch = ''
if ($null -ne $Target) {
    if ($Target.Length -gt 0) { $TargetOS = $Target[0] }
    if ($Target.Length -gt 1) { $TargetArch = $Target[1] }
}

# Build from the script's own directory so the result does not depend on the
# caller's current location, and so relative paths in -o stay predictable.
Set-Location -LiteralPath $PSScriptRoot

function Resolve-Target {
    param(
        [string]$Given,
        [string]$EnvName,
        [string]$Label
    )

    if (-not [string]::IsNullOrWhiteSpace($Given)) { return $Given }

    $value = (& go env $EnvName).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($value)) {
        throw "cannot determine the host $Label from 'go env $EnvName'"
    }
    return $value
}

$goos = Resolve-Target -Given $TargetOS -EnvName GOOS -Label 'operating system'
$goarch = Resolve-Target -Given $TargetArch -EnvName GOARCH -Label 'architecture'

$extension = if ($goos -eq 'windows') { '.exe' } else { '' }
$name = "bin2source_${goos}_${goarch}${extension}"
if ($goos -eq (go env GOOS) -and $goarch -eq (go env GOARCH)) {
    $name = "bin2source${extension}"
}

New-Item -ItemType Directory -Path bin -Force | Out-Null
$output = Join-Path 'bin' $name

Write-Host "building $output"

# Same flags as build.sh: -trimpath for reproducible paths inside the binary,
# and stripped symbols and DWARF to keep the single file small.
$env:CGO_ENABLED = '0'
$env:GOOS = $goos
$env:GOARCH = $goarch

try {
    & go build -trimpath -ldflags='-s -w' -o $output ./src
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }
}
catch {
    # A half written binary is worse than none: it looks like a build.
    Remove-Item -LiteralPath $output -Force -ErrorAction SilentlyContinue
    throw
}

Write-Host "wrote $output ($((Get-Item -LiteralPath $output).Length) bytes)"
