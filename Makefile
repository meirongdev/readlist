# readlist — build & test & kind E2E
# 约定:Go 包路径用 ./cmd/... ./internal/...,不用 ./...(否则会扫到 dist 等杂散文件)。
# 依赖装好后 Go 构建/测试全程离线(GOPROXY=off);Docker/kind 步骤需本机 Docker。

GO        ?= go
GOPKGS    := ./cmd/... ./internal/...
GOFMT_DIRS := cmd internal
OFFLINE   := GOPROXY=off
BIN       := bin/readlist
IMG       ?= readlist:dev
PLATFORMS ?= linux/amd64,linux/arm64
KIND_NAME ?= readlist
SPA_PORT  ?= 8099
SPA_TMP   := .tmp-spa
PIPE_DIR  ?= .tmp-pipeline
PIPE_DB   ?= $(PIPE_DIR)/readlist.db

.DEFAULT_GOAL := help
.PHONY: help build run test test-go test-race test-spa fmt fmt-check vet \
        check clean docker-build docker-buildx kind-up kind-down kind-load \
        deploy deploy-clean e2e e2e-serve smoke pipeline snapshot

help: ## 列出可用目标
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# ---- 构建 ----
build: ## 构建单二进制 → bin/readlist
	$(OFFLINE) $(GO) build -o $(BIN) ./cmd/readlist

run: build ## 构建并以默认 DB 运行(生产形态:单二进制托管内嵌 SPA)
	./$(BIN) serve

# ---- 测试 ----
test: test-go test-spa ## 跑全部测试(Go + SPA 一致性)
test-go: ## Go 测试(离线)
	$(OFFLINE) $(GO) test $(GOPKGS)
test-race: ## Go 测试(竞态检测)
	$(OFFLINE) $(GO) test -race $(GOPKGS)

test-spa: build ## 校验 SPA 客户端重排与后端公式逐位一致(需要 node,缺则跳过)
	@if ! command -v node >/dev/null 2>&1; then echo "  跳过 test-spa:未安装 node"; exit 0; fi; \
	rm -rf $(SPA_TMP) && mkdir -p $(SPA_TMP); \
	DB_PATH=$(SPA_TMP)/spa.db ./$(BIN) seed >/dev/null && \
	DB_PATH=$(SPA_TMP)/spa.db ./$(BIN) score >/dev/null && \
	( DB_PATH=$(SPA_TMP)/spa.db API_LISTEN_ADDR=:$(SPA_PORT) ./$(BIN) serve >$(SPA_TMP)/serve.log 2>&1 & \
	  echo $$! >$(SPA_TMP)/pid ); \
	for i in 1 2 3 4 5 6 7 8 9 10; do \
	  curl -sf http://127.0.0.1:$(SPA_PORT)/healthz >/dev/null 2>&1 && break || sleep 0.3; \
	done; \
	BASE=http://127.0.0.1:$(SPA_PORT) node scripts/spa-parity.js; rc=$$?; \
	kill $$(cat $(SPA_TMP)/pid) 2>/dev/null || true; \
	rm -rf $(SPA_TMP); \
	exit $$rc

# ---- 格式化 / 静态检查 ----
fmt: ## Go 格式化(gofmt,会改写文件)
	$(GO) fmt $(GOPKGS)
fmt-check: ## 检查 Go 格式(有未格式化文件则失败)
	@out=$$(gofmt -l $(GOFMT_DIRS)); \
	if [ -n "$$out" ]; then echo "以下文件需要 gofmt:"; echo "$$out"; exit 1; fi
vet: ## go vet
	$(OFFLINE) $(GO) vet $(GOPKGS)
check: fmt-check vet test-go test-spa ## 总检查(不改写)

# ---- 镜像 ----
# 本地目标必须让镜像真的落到 docker daemon 里 —— 之前 docker-build 用 buildx 跑
# 多架构却既不 --load 也不 --push,镜像哪儿都没有,紧接着的 kind load 必然失败。
docker-build: ## 构建本机架构镜像并载入本地 daemon → $(IMG)
	docker build -t $(IMG) .

docker-buildx: ## 构建并**推送**多架构镜像($(PLATFORMS));集群要 linux/arm64
	docker buildx build --platform $(PLATFORMS) -t $(IMG) --push .

# ---- kind 本地端到端 ----
kind-up: ## 创建 kind 集群(不存在才创建)
	kind get clusters 2>/dev/null | grep -qx "$(KIND_NAME)" || kind create cluster \
		--name "$(KIND_NAME)" --config deploy/kind/kind-config.yaml

kind-down: ## 删除 kind 集群
	kind delete cluster --name "$(KIND_NAME)"

kind-load: docker-build ## 构建镜像并装入 kind 集群
	kind load docker-image $(IMG) --name "$(KIND_NAME)"

deploy: kind-load ## 把 readlist 部署到 kind(先 load 镜像,再 apply 清单)
	kubectl --context kind-$(KIND_NAME) apply -k deploy/kind
	kubectl --context kind-$(KIND_NAME) -n readlist rollout status deploy/readlist --timeout=180s

deploy-clean: ## 卸载 kind 上的 readlist
	kubectl --context kind-$(KIND_NAME) delete -k deploy/kind --ignore-not-found=true

e2e: ## 完整本地端到端:kind-up + deploy + 断言 API(推荐入口)
	./scripts/e2e-kind.sh

e2e-serve: ## 打印 NodePort 访问地址(取决于 e2e 已部署)
	@echo "http://localhost:$(shell kubectl --context kind-$(KIND_NAME) -n readlist get svc readlist -o jsonpath='{.spec.ports[0].nodePort}')"

smoke: build ## 本地直跑 seed + score + dryrun(演示语料,不经过 k8s)
	rm -f readlist.db readlist.db-wal readlist.db-shm
	./$(BIN) seed && ./$(BIN) score && ./$(BIN) dryrun

# 对着真实 calibre 库演练生产管道。两个源库路径必须显式给,避免误连生产卷。
#   make pipeline SOURCE_METADATA_DB=/path/metadata.db SOURCE_APP_DB=/path/app.db
# ingest 需要出网;只想验证 snapshot 就用 make snapshot。
pipeline: snapshot ## snapshot + ingest + score(需要 SOURCE_METADATA_DB / SOURCE_APP_DB)
	DB_PATH=$(PIPE_DB) ./$(BIN) ingest
	DB_PATH=$(PIPE_DB) ./$(BIN) score

snapshot: build ## 只跑 snapshot(读 calibre 两库 → 真实语料,不联网)
	@test -n "$(SOURCE_METADATA_DB)" || { echo "需要 SOURCE_METADATA_DB=/path/to/metadata.db"; exit 2; }
	DB_PATH=$(PIPE_DB) SOURCE_METADATA_DB=$(SOURCE_METADATA_DB) \
	  SOURCE_APP_DB=$(SOURCE_APP_DB) SNAPSHOT_DIR=$(PIPE_DIR)/snapshot ./$(BIN) snapshot

clean: ## 清理构建产物
	rm -rf bin $(SPA_TMP) $(PIPE_DIR)
