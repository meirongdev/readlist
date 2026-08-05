#!/usr/bin/env bash
# readlist 本地端到端:kind 集群 + 部署 + API 断言。
# 依赖:docker / kind / kubectl / python3(node 可选,用于 SPA 一致性校验)。
set -euo pipefail

KIND_NAME="${KIND_NAME:-readlist}"
IMG="${IMG:-readlist:dev}"
PORT="${PORT:-30080}"
CTX="kind-${KIND_NAME}"
NS="readlist"

say()  { printf '\033[36m[readlist-e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[31m[readlist-e2e] FAIL:\033[0m %s\n' "$*"; exit 1; }
pass() { printf '\033[32m[readlist-e2e] ok:\033[0m %s\n' "$*"; }

command -v docker  >/dev/null || fail "docker 未安装"
command -v kind    >/dev/null || fail "kind 未安装"
command -v kubectl >/dev/null || fail "kubectl 未安装"
command -v python3 >/dev/null || fail "python3 未安装"

K() { kubectl --context "$CTX" -n "$NS" "$@"; }

# ---- 断言助手 ----
# 一律「先把内容抓进变量,再判断」,不做 `curl … | grep -q`:grep -q 命中即退出,
# 上游 curl 收到 EPIPE 非零退出,`set -o pipefail` 就会把成功的断言判成失败
# ——  响应体越大越容易踩到(matrix 有几十 KB)。
jexpr()  { printf '%s' "$1" | python3 -c "import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1]))" "$2"; }
want()   { [ "$(jexpr "$1" "$2")" = "True" ] || fail "$3"; }
has()    { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }
GET()    { curl -sf "$BASE$1" || fail "$1 不可达"; }
CODE()   { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# 1) 集群
if kind get clusters 2>/dev/null | grep -qx "$KIND_NAME"; then
  say "kind 集群已存在:$KIND_NAME(复用)"
else
  say "创建 kind 集群 $KIND_NAME"
  kind create cluster --name "$KIND_NAME" --config deploy/kind/kind-config.yaml
fi
kubectl --context "$CTX" cluster-info >/dev/null

# NodePort 通过节点 IP 暴露(新建集群时 kind 的端口映射也让 localhost 可用)。
NODE_IP="$(kubectl --context "$CTX" get node -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
BASE="http://${NODE_IP:-localhost}:${PORT}"

# 2) 镜像
say "构建镜像 $IMG"
docker build -t "$IMG" .
say "装载镜像进集群"
kind load docker-image "$IMG" --name "$KIND_NAME"

# 3) 部署
# 默认从空 PVC 起,才真正验证 initContainer 这条生产路径:复用集群时旧数据卷上留着
# **上一版二进制**打出来的 run,而 `readlist init` 是幂等的(已发布过就跳过打分),
# 于是断言会打在陈旧产物上。
if [ "${E2E_KEEP_DATA:-0}" = "1" ]; then
  say "保留既有数据卷(E2E_KEEP_DATA=1)"
else
  say "重置 Deployment 与数据卷(设 E2E_KEEP_DATA=1 可跳过)"
  K delete deploy readlist --ignore-not-found --wait >/dev/null 2>&1 || true
  # 已完成的 Job pod 仍算 PVC 的使用者,会让 PVC 卡在 Terminating ——  必须先清掉,
  # 否则 `delete pvc --wait` 会永久阻塞。
  K delete jobs --all --ignore-not-found >/dev/null 2>&1 || true
  K delete pvc readlist-data --ignore-not-found --wait=false >/dev/null 2>&1 || true
  for _ in $(seq 1 30); do K get pvc readlist-data >/dev/null 2>&1 || break; sleep 1; done
  if K get pvc readlist-data >/dev/null 2>&1; then
    say "  注意:PVC 未能在 30s 内释放,继续(后面会强制重算一次)"
  fi
fi

say "应用清单(kustomize)"
kubectl --context "$CTX" apply -k deploy/kind
# 镜像 tag 不变但内容变了时,集群看 spec 没变化 → 不会滚动更新,旧 pod 继续跑。
# 少了这一步,复用集群的 e2e 会静默地验证上一次构建的二进制。
say "强制重启以确保跑的是刚构建的镜像"
K rollout restart deploy/readlist >/dev/null
say "等待 Deployment 就绪"
K rollout status deploy/readlist --timeout=180s

# 用 CronJob 自己的定义跑一次重算。两个作用:验证夜间路径(否则 CronJob 的 env 与
# 卷挂载全程没被测过),并保证后面的断言一定打在**当前二进制**的产物上。
say "按 CronJob 定义触发一次重算"
RUN_BEFORE="$(jexpr "$(GET /api/v1/meta)" "d.get('run_id','')")"
JOB="e2e-score-$$"
K create job "$JOB" --from=cronjob/readlist-score >/dev/null
K wait --for=condition=complete "job/$JOB" --timeout=120s >/dev/null \
  || { K logs "job/$JOB" || true; fail "CronJob 定义的重算失败"; }
# 必须删掉:已完成的 Job pod 会持有 PVC,让下一次 e2e 的 PVC 删除卡死。
K delete job "$JOB" --wait >/dev/null 2>&1 || true

# 4) 断言
say "检查 /healthz(须有 run_id 与 corpus_id)"
H=$(GET /healthz)
want "$H" "d['status']=='ok'"       "healthz status != ok"
want "$H" "bool(d['run_id'])"       "healthz 无 run_id"
want "$H" "bool(d['corpus_id'])"    "healthz 缺 corpus_id(语料指纹没落到 run 上)"

say "检查 /api/v1/meta:run_id 与 standard_version,且重算已原子换榜"
M=$(GET /api/v1/meta)
RUN="$(jexpr "$M" "d['run_id']")"
[ -n "$RUN" ] || fail "meta 无 run_id"
want "$M" "bool(d['standard_version'])" "meta 缺 standard_version"
if [ -n "$RUN_BEFORE" ] && [ "$RUN_BEFORE" = "$RUN" ]; then
  fail "CronJob 重算后 run_id 没变($RUN)—— 原子发布没生效"
fi

say "检查 /api/v1/lists:公开榜不含 internal,且每份榜都带滑块所需口径"
L=$(GET /api/v1/lists)
IDS="$(jexpr "$L" "' '.join(x['id'] for x in d['lists'])")"
has "$IDS" "timeless"         || fail "lists 缺 timeless"
has "$IDS" "library-hygiene"  && fail "internal 榜泄漏到公开列表"
# 权重滑块的数据契约:缺了 weights,前端会把每本书都算成 0 分,而首页看不出报错。
want "$L" "all(x.get('weights') and abs(sum(x['weights'].values())-1)<1e-6 for x in d['lists'])" \
  "lists 缺 weights 或权重和不为 1"
want "$L" "all(x.get('order') in ('desc','asc') and 'min_coverage' in x for x in d['lists'])" \
  "lists 缺 order / min_coverage"
want "$L" "all(set(x.get('bands') or {}) <= set(x['weights']) for x in d['lists'])" \
  "有 band 维度没有权重 → band 是空操作"

say "检查 /api/v1/lists/timeless:内容、TBS、理由串、coverage"
T=$(GET /api/v1/lists/timeless)
want "$T" "len(d['items'])>=1" "timeless 为空"
want "$T" "all(x['tbs']>0 and x['reason'] and x['coverage']>=0.7-1e-9 for x in d['items'])" \
  "timeless 有条目 TBS 非正 / 缺理由 / coverage 低于门槛"
# review B1 的回归:timeless 不使用时效维度,所以 pubdate 不可信的书必须能进来。
want "$T" "any(x['dims'].get('F',{}).get('state')=='unknown' for x in d['items'])" \
  "F 不可信的书被挡在 timeless 之外(证据字母又变成全局闸门了?)"

say "检查 /api/v1/works/{id}:得分拆解 + 缺失维度说明 + 外链"
WID="$(jexpr "$T" "d['items'][0]['work_id']")"
WID_ENC="$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$WID")"
W=$(GET "/api/v1/works/$WID_ENC")
want "$W" "bool(d['dims']) and bool(d['standard_version']) and 'editions' in d" \
  "work 详情缺 dims / standard_version / editions"
want "$W" "all(v.startswith('https://') and ' ' not in v for v in d['links'].values())" \
  "work 外链未转义或不是 https"

say "检查 /api/v1/catalog:收录全库并逐本标注缺失维度"
C=$(GET /api/v1/catalog)
want "$C" "d['total']>=1"                  "catalog 为空"
want "$C" "d['total']==len(d['works'])"    "catalog total 与行数不符"
# 缺维度的书不该被静默剔除,而该带着「缺哪几维」出现在目录里(system-design §2)。
want "$C" "any(x.get('missing') for x in d['works'])" \
  "没有任何一本书被标注缺失维度(D 级书是不是又被过滤掉了?)"

say "检查 /metrics 指标"
MET=$(GET /metrics)
for m in readlist_works_total readlist_grade_counts readlist_lists_total \
         readlist_runs_retained readlist_last_score_unix; do
  has "$MET" "$m" || fail "metrics 缺 $m"
done
# 新鲜度 / 判别力 / 数据质量。只看 last_score 是不够的:score 在陈旧 facts 上每晚
# 照样成功,snapshot 或 ingest 挂掉一个月它依然常绿。
for m in readlist_last_snapshot_unix readlist_last_ingest_unix readlist_dim_measured \
         readlist_pubdate_source readlist_orphan_rows readlist_ingest_requests; do
  has "$MET" "$m" || fail "metrics 缺 $m"
done

say "检查 ETag/304:内容按 run 不可变,爬虫不该每次都打到源站"
ETAG=$(curl -s -D - -o /dev/null "$BASE/api/v1/lists/timeless" | tr -d '\r' \
        | awk 'tolower($1)=="etag:"{print $2}')
[ -n "$ETAG" ] || fail "/api/v1/lists/timeless 缺 ETag"
code=$(CODE -H "If-None-Match: $ETAG" "$BASE/api/v1/lists/timeless")
[ "$code" = "304" ] || fail "带 If-None-Match 应回 304(实际 $code)"

say "检查存活探针:必须不碰数据库(否则高负载时 kubelet 会杀掉唯一副本)"
[ "$(CODE "$BASE/livez")" = "200" ] || fail "/livez 不可达"

say "检查 matrix:真实 run 可访问且长缓存,未知 run 必须 404"
MX=$(GET "/api/v1/matrix/$RUN")
want "$MX" "len(d['works'])>=1" "matrix 为空"
# -D - + -o /dev/null:只取响应头,且让 curl 把响应体读完(不制造 EPIPE)。
MXH=$(curl -s -D - -o /dev/null "$BASE/api/v1/matrix/$RUN")
has "$MXH" "public, max-age=31536000, immutable" || fail "matrix 缺 immutable 缓存头"
[ "$(CODE "$BASE/api/v1/matrix/no-such-run")" = "404" ] \
  || fail "未知 run 应 404,否则空矩阵会被永久缓存"

say "检查零写接口:POST/PUT/DELETE 一律 405"
for verb in POST PUT DELETE; do
  code=$(CODE -X "$verb" "$BASE/api/v1/lists/timeless")
  [ "$code" = "405" ] || fail "$verb 未返回 405(实际 $code)"
done

say "检查 internal 榜不能按 id 直接拉到"
[ "$(CODE "$BASE/api/v1/lists/library-hygiene")" = "404" ] || fail "internal 榜可被直接请求"

say "检查 read-and-loved 有内容(演示补录个人星级后)"
want "$(GET /api/v1/lists/read-and-loved)" "len(d['items'])>=1" "read-and-loved 为空"

say "检查 SPA 首页"
# 变量名别用 HOME —— 覆盖它会让后续的 kubectl 找不到 $HOME/.kube/config,
# 报出「context 不存在」这种与真实原因毫无关系的错。
HOMEPAGE=$(GET /)
has "$HOMEPAGE" "readlist" || fail "SPA 首页未返回"

if command -v node >/dev/null 2>&1; then
  # 走 port-forward 而不是上面那个 NodePort 地址,绕开两个与被测代码无关的环境问题:
  #   1. hostPort 30080 可能已被本机另一个 kind 集群占用,localhost 会打到别人身上;
  #   2. node 的 fetch(undici)在 Docker 网桥网段上会挑一个无效源地址 → EHOSTUNREACH,
  #      而同一地址 curl 是通的。
  PF_PORT="${PF_PORT:-38080}"
  PF_LOG="$(mktemp -t readlist-pf)"
  say "校验 SPA 客户端重排与后端公式一致(port-forward :$PF_PORT)"
  K port-forward svc/readlist "${PF_PORT}:80" >"$PF_LOG" 2>&1 &
  PF_PID=$!
  # shellcheck disable=SC2064
  trap "kill $PF_PID 2>/dev/null || true; rm -f '$PF_LOG'" EXIT
  for _ in $(seq 1 40); do
    curl -sf "http://127.0.0.1:${PF_PORT}/healthz" >/dev/null 2>&1 && break
    kill -0 "$PF_PID" 2>/dev/null || break   # port-forward 已死,不必再等
    sleep 0.5
  done
  if ! curl -sf "http://127.0.0.1:${PF_PORT}/healthz" >/dev/null 2>&1; then
    say "  port-forward 输出:"; sed 's/^/    /' "$PF_LOG" || true
    fail "port-forward 未就绪(端口 $PF_PORT 可能被占用,可用 PF_PORT=… 换一个)"
  fi
  BASE="http://127.0.0.1:${PF_PORT}" node scripts/spa-parity.js || fail "SPA 与后端评分口径不一致"
  kill "$PF_PID" 2>/dev/null || true
  trap - EXIT
else
  say "跳过 SPA 一致性校验(未安装 node)"
fi

echo
pass "全部断言通过 —— kind 端到端验证成功"
pass "浏览入口: ${BASE}/   (新建集群时 localhost:${PORT} 亦可)"
