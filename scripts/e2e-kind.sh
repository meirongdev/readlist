#!/usr/bin/env bash
# readlist 本地端到端:kind 集群 + 部署 + API 断言。
# 依赖:docker / kind / kubectl。全部命令在本机执行(需能访问 docker socket)。
set -euo pipefail

KIND_NAME="${KIND_NAME:-readlist}"
IMG="${IMG:-readlist:dev}"
PORT="${PORT:-30080}"
CTX="kind-${KIND_NAME}"
NS="readlist"
# kind 的 NodePort 通过节点 IP 暴露(默认无 localhost 端口映射)。
NODE_IP="$(kubectl --context "$CTX" get node -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
BASE="http://${NODE_IP:-localhost}:${PORT}"

say()  { printf '\033[36m[readlist-e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[readlist-e2e] FAIL:\033[0m %s\n' "$*"; exit 1; }
pass() { printf '\033[32m[readlist-e2e] ok:\033[0m %s\n' "$*"; }

command -v docker >/dev/null || fail "docker 未安装"
command -v kind   >/dev/null || fail "kind 未安装"
command -v kubectl>/dev/null || fail "kubectl 未安装"
command -v python3>/dev/null || fail "python3 未安装"

# 1) 集群
if kind get clusters 2>/dev/null | grep -qx "$KIND_NAME"; then
  say "kind 集群已存在:$KIND_NAME(复用)"
else
  say "创建 kind 集群 $KIND_NAME"
  kind create cluster --name "$KIND_NAME" --config deploy/kind/kind-config.yaml
fi
kubectl --context "$CTX" cluster-info >/dev/null

# 2) 镜像
say "构建镜像 $IMG"
docker build -t "$IMG" .
say "装载镜像进集群"
kind load docker-image "$IMG" --name "$KIND_NAME"

# 3) 部署
say "应用清单(kustomize)"
kubectl --context "$CTX" apply -k deploy/kind
say "等待 Deployment 就绪"
kubectl --context "$CTX" -n "$NS" rollout status deploy/readlist --timeout=180s

# 4) 断言
jget() { python3 -c "import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1]))" "$1"; }

say "检查 /healthz"
H=$(curl -sf "$BASE/healthz" || fail "/healthz 不可达")
[ "$(echo "$H" | jget "d['status']")" = "ok" ] || fail "healthz status != ok"

say "检查 /api/v1/meta 有 run_id"
M=$(curl -sf "$BASE/api/v1/meta" || fail "meta 不可达")
RUN=$(echo "$M" | jget "d['run_id']")
[ -n "$RUN" ] || fail "meta 无 run_id"

say "检查 /api/v1/lists 公开榜(应不含 library-hygiene)"
L=$(curl -sf "$BASE/api/v1/lists" || fail "lists 不可达")
IDS=$(echo "$L" | jget "' '.join(x['id'] for x in d['lists'])")
echo "$IDS" | grep -q "timeless"  || fail "lists 缺 timeless"
echo "$IDS" | grep -q "library-hygiene" && fail "internal 榜泄漏到公开列表"

say "检查 /api/v1/lists/timeless 内容"
T=$(curl -sf "$BASE/api/v1/lists/timeless" || fail "timeless 不可达")
CNT=$(echo "$T" | jget "len(d['items'])")
[ "$CNT" -ge 1 ] || fail "timeless 为空"
G=$(echo "$T" | jget "d['items'][0]['grade']")
[ "$G" != "D" ] || fail "榜单出现 D 级书"
TBS=$(echo "$T" | jget "d['items'][0]['tbs']")
[ "$(echo "$TBS" | python3 -c 'import sys; print("1" if float(sys.stdin.read()) > 0 else "0")')" = "1" ] || fail "TBS 非正($TBS)"

say "检查 /api/v1/works/{id} 得分拆解"
WID=$(echo "$T" | jget "d['items'][0]['work_id']")
WID_ENC=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1], safe=''))" "$WID")
W=$(curl -sf "$BASE/api/v1/works/$WID_ENC" || fail "work 不可达")
echo "$W" | jget "d['dims'].get('A',{}).get('state','')" >/dev/null || fail "work 缺 dims"
echo "$W" | grep -q "standard_version" || fail "work 缺 standard_version"

say "检查 /api/v1/catalog(含 C 级,不含 D 级)"
C=$(curl -sf "$BASE/api/v1/catalog" || fail "catalog 不可达")
cat_total=$(echo "$C" | jget "d['total']")
cat_d=$(echo "$C" | jget "sum(1 for x in d['works'] if x['grade']=='D')")
[ "$cat_d" = "0" ] || fail "catalog 出现 D 级书"
[ "$cat_total" -ge 1 ] || fail "catalog 为空"

say "检查 /metrics 指标"
curl -sf "$BASE/metrics" | grep -q "readlist_grade_counts" || fail "metrics 缺 readlist_grade_counts"
curl -sf "$BASE/metrics" | grep -q "readlist_last_score_unix" || fail "metrics 缺 last_score_unix"

say "检查 SPA 首页"
curl -sf "$BASE/" | grep -q "readlist" || fail "SPA 首页未返回"

say "检查零写接口:POST 应 405"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/lists/timeless")
[ "$CODE" = "405" ] || fail "POST 未返回 405(实际 $CODE)"

say "检查内部榜只读镜像:read-and-loved 有内容(演示补录后)"
RL=$(curl -sf "$BASE/api/v1/lists/read-and-loved" || fail "read-and-loved 不可达")
[ "$(echo "$RL" | jget "len(d['items'])")" -ge 1 ] || fail "read-and-loved 为空"

say "检查 matrix 滑块数据可访问"
curl -sf "$BASE/api/v1/matrix/$RUN" | jget "len(d['works'])" >/dev/null || fail "matrix 不可达"

echo
pass "全部断言通过 —— kind 端到端验证成功"
pass "浏览入口: ${BASE}/   (新建集群时 localhost:${PORT} 亦可)"
