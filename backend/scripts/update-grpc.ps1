$ErrorActionPreference = "Stop"

$backendRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $backendRoot
try {
    foreach ($command in @("protoc", "protoc-gen-go", "protoc-gen-go-grpc")) {
        if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
            throw "$command is required and must be available on PATH"
        }
    }

    & protoc `
        --proto_path=. `
        --go_out=. `
        --go_opt=module=vue-element-plus-admin/backend `
        --go-grpc_out=. `
        --go-grpc_opt=module=vue-element-plus-admin/backend `
        proto/department.proto `
        proto/user_management.proto
}
finally {
    Pop-Location
}
