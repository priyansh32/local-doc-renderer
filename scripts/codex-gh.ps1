[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$GhArgs
)

$ErrorActionPreference = "Stop"

function Get-ArthurToken {
    param(
        [Parameter(Mandatory = $true)]
        [string]$EnvPath
    )

    if (-not (Test-Path -LiteralPath $EnvPath)) {
        throw ".env not found at $EnvPath"
    }

    $line = Get-Content -LiteralPath $EnvPath |
        Where-Object { $_ -match '^\s*ARTHUR_TOKEN\s*=' } |
        Select-Object -First 1

    if (-not $line) {
        throw "ARTHUR_TOKEN not found in $EnvPath"
    }

    $token = ($line -split '=', 2)[1].Trim()
    if (
        ($token.StartsWith('"') -and $token.EndsWith('"')) -or
        ($token.StartsWith("'") -and $token.EndsWith("'"))
    ) {
        $token = $token.Substring(1, $token.Length - 2)
    }

    if ([string]::IsNullOrWhiteSpace($token)) {
        throw "ARTHUR_TOKEN is empty in $EnvPath"
    }

    return $token
}

function Restore-EnvVar {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,
        [string]$Value,
        [Parameter(Mandatory = $true)]
        [bool]$WasSet
    )

    if ($WasSet) {
        Set-Item -Path "Env:$Name" -Value $Value
        return
    }

    Remove-Item -Path "Env:$Name" -ErrorAction SilentlyContinue
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$envPath = Join-Path $repoRoot ".env"
$token = Get-ArthurToken -EnvPath $envPath

$prevGhToken = $env:GH_TOKEN
$hadGhToken = Test-Path Env:GH_TOKEN
$prevGhHost = $env:GH_HOST
$hadGhHost = Test-Path Env:GH_HOST
$prevPromptDisabled = $env:GH_PROMPT_DISABLED
$hadPromptDisabled = Test-Path Env:GH_PROMPT_DISABLED

$env:GH_TOKEN = $token
$env:GH_HOST = "github.com"
$env:GH_PROMPT_DISABLED = "1"

try {
    $login = (& gh api user --jq ".login" 2>$null).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($login)) {
        throw "Failed to validate GitHub auth with ARTHUR_TOKEN."
    }

    if ($login -ne "realArthurMorgan") {
        throw "ARTHUR_TOKEN resolves to '$login', expected 'realArthurMorgan'."
    }

    if (-not $GhArgs -or $GhArgs.Count -eq 0) {
        & gh auth status
        exit $LASTEXITCODE
    }

    & gh @GhArgs
    exit $LASTEXITCODE
}
finally {
    Restore-EnvVar -Name "GH_TOKEN" -Value $prevGhToken -WasSet $hadGhToken
    Restore-EnvVar -Name "GH_HOST" -Value $prevGhHost -WasSet $hadGhHost
    Restore-EnvVar -Name "GH_PROMPT_DISABLED" -Value $prevPromptDisabled -WasSet $hadPromptDisabled
}
