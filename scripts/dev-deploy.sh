#!/usr/bin/env bash
# JustNav 自用版一键部署（8524）
#
#   ./scripts/dev-deploy.sh              构建当前源码 → 替换 bin → 重建容器
#   ./scripts/dev-deploy.sh --no-restart 只构建，不动容器
#
# 干了什么：
#   1. 从当前源码编出两个静态二进制，落到仓库存放的 bin/
#   2. 注入版本号 dev-<短哈希>[-dirty]，让 /api/health 一眼看出跑的是哪个 commit
#   3. 用 compose.selfhost.yaml 重建容器（挂载变了才会重建，否则只重启）
#   4. 打一次 /api/health 自检
#
# 为什么先写 .tmp 再 mv：
#   容器正在执行 bin/justnav。直接覆盖会拿到 "Text file busy"，
#   或者更糟——写到一半被 mmap 进去，跑起来就是段错误。
#   mv 在同一个文件系统上是原子的，容器下一次 exec 拿到的必然是新文件。

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${REPO_ROOT}/bin"
COMPOSE_FILE="${REPO_ROOT}/compose.selfhost.yaml"
HEALTH_URL="${JUSTNAV_HEALTH_URL:-http://127.0.0.1:8524/api/health}"

RESTART=1
for arg in "$@"; do
  case "$arg" in
    --no-restart) RESTART=0 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数: $arg" >&2; exit 2 ;;
  esac
done

cd "$REPO_ROOT"
mkdir -p "$BIN_DIR"

# ---- 版本号 ----
SHA="$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
DIRTY=""
if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
  DIRTY="-dirty"
fi
VERSION="dev-${SHA}${DIRTY}"

echo "→ 版本 $VERSION"

# ---- 构建 ----
CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -X justnav/internal/api.Version=${VERSION}" \
  -o "${BIN_DIR}/justnav.tmp" .

CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w" \
  -o "${BIN_DIR}/justnav-discovery.tmp" ./cmd/justnav-discovery

mv -f "${BIN_DIR}/justnav.tmp"           "${BIN_DIR}/justnav"
mv -f "${BIN_DIR}/justnav-discovery.tmp" "${BIN_DIR}/justnav-discovery"

# 755 不是可选项：go build 按 umask 产出 700，而 justnav-discovery 容器
# 带 cap_drop:[ALL]，丢了 CAP_DAC_OVERRIDE，root 也读不了 700 的文件 ——
# 报的是 "stat /app/justnav-discovery: permission denied"，看着像挂载错了。
chmod 755 "$BIN_DIR" "${BIN_DIR}/justnav" "${BIN_DIR}/justnav-discovery"

echo "→ 已写入 ${BIN_DIR}/{justnav,justnav-discovery}"

[ "$RESTART" = "1" ] || { echo "→ --no-restart，跳过部署"; exit 0; }

# ---- 部署 ----
echo "→ compose up -d"
docker compose -f "$COMPOSE_FILE" up -d

# ---- 自检 ----
for i in $(seq 1 15); do
  sleep 1
  body="$(curl -s --noproxy '*' -m 5 "$HEALTH_URL" 2>/dev/null || true)"
  if [ -n "$body" ]; then
    echo "→ health: $body"
    case "$body" in
      *"\"version\":\"${VERSION}\""*) echo "✓ 版本对上，部署成功" ; exit 0 ;;
      *) echo "⚠ 起来了，但版本不是 ${VERSION} —— 检查挂载是否生效" >&2 ; exit 1 ;;
    esac
  fi
done

echo "✗ 15 秒内 health 没响应" >&2
docker logs --tail 20 justnav 2>&1 >&2 || true
exit 1
