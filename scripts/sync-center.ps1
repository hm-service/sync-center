#Requires -Version 7.0
# sync-center 客户端模板。
# 用法：改下面的【配置】块，再改末尾【主流程】里标记的那一步，其余不用动。
# 协议见 DESIGN.md 第 11 节；中间的函数是协议实现，换成自己的客户端时可以整段拿走。

# ================= 配置 =================
$Server = 'http://localhost:18080'          # sync-center 地址
$Token  = 'TOKENRW'                         # namespace 的 token
$Root   = Join-Path $env:APPDATA 'Rime'     # 同步根目录

# 先看 Exclude，再看 Include，都不命中则不同步。
# /*.yaml 匹配根目录单层，/cn_dicts/** 匹配整棵目录，** 表示全部。
$Include = @(
    '/*.yaml',
    '/*.txt',
    '/cn_dicts/**',
    '/en_dicts/**',
    '/lua/**',
    '/opencc/**',
    '/sync/**'
)

$Exclude = @(
    '/build/**',
    '/installation.yaml',
    '/user.yaml'
)

$Delete = $false                            # 拉取时把远端没有的本地文件移入 .sync-trash
$Force  = $false                            # 忽略 mtime，强制以远端为准
# ========================================

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

$Base      = $Server.TrimEnd('/')
$Root      = [System.IO.Path]::GetFullPath($Root)
$StateFile = Join-Path $Root '.sync-center.json'
$StateName = '.sync-center.json'
$TrashRoot = Join-Path $Root '.sync-trash'
$StageRoot = Join-Path $Root '.sync-staging'
$Headers   = @{ Authorization = "Bearer $Token" }

$Separator = [System.IO.Path]::DirectorySeparatorChar

# ================= 协议实现（一般不用改） =================

function Test-Pattern([string]$relative, [string]$pattern) {
    $p = $pattern.TrimStart('/')

    if ($p -eq '**') { return $true }

    if ($p.EndsWith('/**')) {
        $prefix = $p.Substring(0, $p.Length - 3)
        return $relative -eq $prefix -or $relative.StartsWith($prefix + '/')
    }

    if ($p -eq '*') { return $relative -notmatch '/' }

    if ($p.Contains('*')) {
        $regex = '^' + ([regex]::Escape($p) -replace '\\\*', '[^/]*') + "$"
        return $relative -match $regex
    }

    return $relative -eq $p
}

function Test-Included([string]$relative) {
    foreach ($pattern in $Exclude) {
        if (Test-Pattern $relative $pattern) { return $false }
    }
    foreach ($pattern in $Include) {
        if (Test-Pattern $relative $pattern) { return $true }
    }
    return $false
}

function Invoke-Api {
    param(
        [string] $Method,
        [string] $Path,
        [byte[]] $Body
    )
    $params = @{
        Uri         = "$Base$Path"
        Method      = $Method
        Headers     = $Headers
        TimeoutSec  = 120
        ErrorAction = 'Stop'
    }
    if ($null -ne $Body) {
        $params.Body = $Body
        $params.ContentType = 'application/json'
    }
    try {
        $response = Invoke-WebRequest @params
        if ($null -eq $response) {
            return [pscustomobject]@{ Status = 204; Content = '' }
        }
        return [pscustomobject]@{ Status = [int]$response.StatusCode; Content = $response.Content }
    }
    catch {
        if ($null -eq $_.Exception.Response) {
            return [pscustomobject]@{ Status = 0; Content = $_.Exception.Message }
        }
        return [pscustomobject]@{ Status = [int]$_.Exception.Response.StatusCode; Content = $_.ErrorDetails.Message }
    }
}

function Get-RemoteSnapshot {
    # 先取元信息：地址写错（例如把面板地址当 API）时会在这里直接报错，
    # 而不是把 404 当成"远端没有快照"继续往下跑
    $meta = Invoke-Api -Method Get -Path '/v1/metadata'
    if ($meta.Status -eq 0) {
        throw "无法连接 sync-center：$Base（$($meta.Content)）"
    }
    if ($meta.Status -ne 200) {
        throw "获取元信息失败，检查地址与 token：$Base（$($meta.Status) $($meta.Content)）"
    }
    $info = $meta.Content | ConvertFrom-Json -AsHashtable
    if ($info.snapshots.Count -eq 0) { return $null }

    $response = Invoke-Api -Method Get -Path "/v1/snapshots/$($info.snapshots[0].seq)"
    if ($response.Status -ne 200) {
        throw "获取快照失败：$($response.Status) $($response.Content)"
    }
    return $response.Content | ConvertFrom-Json -AsHashtable
}

function Get-Lease {
    for ($attempt = 0; $attempt -lt 3; $attempt++) {
        $response = Invoke-Api -Method Post -Path '/v1/lease'
        if ($response.Status -eq 201) {
            return ($response.Content | ConvertFrom-Json -AsHashtable).leaseId
        }
        if ($response.Status -eq 423) {
            Write-Host 'namespace 被占用，5 秒后重试'
            Start-Sleep -Seconds 5
            continue
        }
        throw "获取租约失败：$($response.Status) $($response.Content)"
    }
    throw 'namespace 持续被占用，放弃'
}

function Get-Blob {
    param([string] $Hash, [string] $Target)
    $parent = Split-Path -Parent $Target
    if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    try {
        Invoke-WebRequest -Uri "$Base/v1/blobs/$Hash" -Headers $Headers -OutFile $Target -TimeoutSec 300 -ErrorAction Stop | Out-Null
    }
    catch {
        Remove-Item -LiteralPath $Target -Force -ErrorAction SilentlyContinue
        throw "下载 blob 失败：$Hash（$($_.Exception.Message)）"
    }
}

function Put-Blob {
    param([string] $Hash, [string] $FilePath)
    try {
        Invoke-WebRequest -Uri "$Base/v1/blobs/$Hash" -Method Put -Headers $Headers -InFile $FilePath -TimeoutSec 300 -ErrorAction Stop | Out-Null
    }
    catch {
        throw "上传 blob 失败：$Hash（$($_.Exception.Message)）"
    }
}

# 扫描 Root，返回 path -> { hash, size, mtime(毫秒) }
function Get-LocalIndex {
    $index = @{}
    Get-ChildItem -LiteralPath $Root -Recurse -File -Force | ForEach-Object {
        $full = $_.FullName
        if ($full -eq $StateFile) { return }
        if ($full.StartsWith($TrashRoot + $Separator)) { return }
        if ($full.StartsWith($StageRoot + $Separator)) { return }

        $relative = [System.IO.Path]::GetRelativePath($Root, $full).Replace('\', '/')
        if (-not (Test-Included $relative)) { return }

        $index[$relative] = @{
            hash  = (Get-FileHash -LiteralPath $full -Algorithm SHA256).Hash.ToLowerInvariant()
            size  = $_.Length
            mtime = [int64]([DateTimeOffset]::new($_.LastWriteTimeUtc)).ToUnixTimeMilliseconds()
        }
    }
    return $index
}

function Read-State {
    if (-not (Test-Path -LiteralPath $StateFile)) {
        return @{ snapshot_seq = 0; files = @{} }
    }
    $state = Get-Content -Raw -Encoding UTF8 -LiteralPath $StateFile | ConvertFrom-Json -AsHashtable
    if (-not $state.ContainsKey('snapshot_seq')) { $state.snapshot_seq = 0 }
    if (-not $state.ContainsKey('files')) { $state.files = @{} }
    return $state
}

function Write-State {
    param($State)
    Set-Content -LiteralPath $StateFile -Value ($State | ConvertTo-Json -Depth 10) -Encoding UTF8
}

# 两阶段：先全部下载到 .sync-staging，全部成功后再逐个原位替换
function Invoke-Pull {
    $state = Read-State
    $local = Get-LocalIndex
    $remote = Get-RemoteSnapshot

    if ($null -eq $remote) {
        # 远端为空说明快照和 blob 都没了：旧 seq 不能再当 parent，
        # 本地状态一并清空，本次按全量重新上传
        $state = @{ snapshot_seq = 0; files = @{} }
        Write-State $state
        Write-Host '远端没有快照，跳过拉取'
        return $state
    }

    $wanted = @{}
    foreach ($entry in $remote.manifest.files) {
        if (Test-Included $entry.path) { $wanted[$entry.path] = $entry }
    }

    if (Test-Path -LiteralPath $StageRoot) {
        Remove-Item -LiteralPath $StageRoot -Recurse -Force
    }

    $kept = 0
    $staged = @{}
    try {
        foreach ($path in $wanted.Keys) {
            $entry = $wanted[$path]
            $target = Join-Path $Root $path

            # mtime LWW：本地较新就保留，交给推送上传
            if (-not $Force -and $local.ContainsKey($path) -and [int64]$local[$path].mtime -gt [int64]$entry.mtime) {
                $kept++
                continue
            }

            $current = $null
            if (Test-Path -LiteralPath $target -PathType Leaf) {
                $current = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
            }

            if ($current -eq $entry.hash) {
                # 内容一致，对齐 mtime，避免下次误判
                $stamp = [DateTimeOffset]::FromUnixTimeMilliseconds([int64]$entry.mtime).UtcDateTime
                $item = Get-Item -LiteralPath $target
                if ($item.LastWriteTimeUtc -ne $stamp) { $item.LastWriteTimeUtc = $stamp }
                continue
            }

            $stage = Join-Path $StageRoot $path
            Get-Blob -Hash $entry.hash -Target $stage
            $staged[$path] = $stage
        }

        foreach ($path in $staged.Keys) {
            $target = Join-Path $Root $path
            $parent = Split-Path -Parent $target
            if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
            Move-Item -LiteralPath $staged[$path] -Destination $target -Force

            $stamp = [DateTimeOffset]::FromUnixTimeMilliseconds([int64]$wanted[$path].mtime).UtcDateTime
            (Get-Item -LiteralPath $target).LastWriteTimeUtc = $stamp
        }
    }
    finally {
        if (Test-Path -LiteralPath $StageRoot) {
            Remove-Item -LiteralPath $StageRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    if ($kept -gt 0) { Write-Host "本地有 $kept 个文件较新，保留本地版本" }

    if ($Delete) {
        $trash = Join-Path $TrashRoot $remote.seq
        foreach ($path in $local.Keys) {
            if ($wanted.ContainsKey($path)) { continue }
            # 本地改过的文件不删，避免丢掉未提交的修改
            if ($state.files.ContainsKey($path) -and [int64]$local[$path].mtime -gt [int64]$state.files[$path].mtime) { continue }
            $source = Join-Path $Root $path
            $destination = Join-Path $trash $path
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
            Move-Item -LiteralPath $source -Destination $destination -Force
        }
    }

    $next = @{ snapshot_seq = $remote.seq; files = @{} }
    foreach ($entry in $wanted.Values) {
        $next.files[$entry.path] = @{ hash = $entry.hash; size = $entry.size; mtime = [int64]$entry.mtime }
    }
    Write-State $next
    Write-Host "拉取完成：seq=$($remote.seq)，下载 $($staged.Count) 个文件"
    return $next
}

function Invoke-Push {
    param($State)
    $local = Get-LocalIndex
    $pending = @()
    foreach ($path in $local.Keys) {
        if (-not $State.files.ContainsKey($path) -or $State.files[$path].hash -ne $local[$path].hash) {
            $pending += $path
        }
    }

    if ($pending.Count -eq 0) {
        Write-Host '没有需要上传的文件'
        return $State
    }

    foreach ($path in $pending) {
        $hash = $local[$path].hash
        $probe = Invoke-Api -Method Head -Path "/v1/blobs/$hash"
        if ($probe.Status -eq 404) {
            Put-Blob -Hash $hash -FilePath (Join-Path $Root $path)
        }
        elseif ($probe.Status -ne 204) {
            throw "探测 blob 失败：$($probe.Status)"
        }
    }

    $lease = Get-Lease
    $files = @()
    foreach ($path in ($local.Keys | Sort-Object)) {
        $files += @{
            path  = $path
            hash  = $local[$path].hash
            size  = $local[$path].size
            mode  = 420
            mtime = [int64]$local[$path].mtime
        }
    }

    $parent = if ($State.snapshot_seq -gt 0) { $State.snapshot_seq } else { $null }
    $payload = @{
        leaseId  = $lease
        parent   = $parent
        manifest = @{ files = $files }
    } | ConvertTo-Json -Depth 10 -Compress

    $body = [System.Text.Encoding]::UTF8.GetBytes($payload)
    $response = Invoke-Api -Method Post -Path '/v1/snapshots' -Body $body
    if ($response.Status -eq 409) { throw '远端 head 已变化，请重新运行' }
    if ($response.Status -ne 201) { throw "提交失败：$($response.Status) $($response.Content)" }

    $result = $response.Content | ConvertFrom-Json -AsHashtable
    $next = @{ snapshot_seq = $result.seq; files = @{} }
    foreach ($path in $local.Keys) {
        $next.files[$path] = @{ hash = $local[$path].hash; size = $local[$path].size; mtime = [int64]$local[$path].mtime }
    }
    Write-State $next
    Write-Host "推送完成：seq=$($result.seq)，上传 $($pending.Count) 个文件"
    return $next
}

# ==========================================================

# ================= 主流程（按需修改） =================
try {
    if (-not (Test-Path -LiteralPath $Root)) {
        New-Item -ItemType Directory -Force -Path $Root | Out-Null
    }

    Write-Host '[1/3] 拉取'
    $state = Invoke-Pull

    # ---- 换成你自己的步骤，不需要就删掉 ----
    Write-Host '[2/3] 自定义步骤'
    # 例如重新部署：& "$env:ProgramFiles\Rime\weasel-x.y.z\WeaselDeployer.exe" /deploy

    Write-Host '[3/3] 推送'
    $state = Invoke-Push -State $state

    Write-Host '完成'
    exit 0
}
catch {
    Write-Host "错误：$($_.Exception.Message)"
    exit 1
}
# ======================================================
