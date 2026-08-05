# readlist — build & test & kind E2E
# 约定:Go 包路径用 ./cmd/... ./internal/...,不用 ./...(否则会扫到 dist 等杂散文件)。
# 依赖装好后 Go 构建/测试全程离线(GOPROXY=off);Docker/kind 步骤需本机 Docker。

GO        ?= go
GOPKGS    := ./cmd/... ./internal/...
GOFMT_DIRS := cmd internal
OFFLINE   := GOPROXY=off
BIN       := bin/readlist
IMG       ?= readlist:dev
KIND_NAME ?= readlist

.DEFAULT_GOAL := help
.PHONY: help setup build run test test-go test-race fmt fmt-check vet \
        check clean docker-build docker-push kind-up kind-down kind-load \
        deploy deploy-clean e2e e2e-serve smoke

help: ## 列出可用目标
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

# ---- 构建 ----
build: ## 构建单二进制 → bin/readlist
	$(OFFLINE) $(GO) build -o $(BIN) ./cmd/readlist

run: build ## 构建并以默认 DB 运行(生产形态:单二进制托管内嵌 SPA)
	./$(BIN) serve

# ---- 测试 ----
test: test-go ## 跑全部测试
test-go: ## Go 测试(离线)
	$(OFFLINE) $(GO) test $(GOPKGS)
test-race: ## Go 测试(竞态检测)
	$(OFFLINE) $(GO) test -race $(GOPKGS)

# ---- 格式化 / 静态检查 ----
fmt: ## Go 格式化(gofmt,会改写文件)
	$(GO) fmt $(GOPKGS)
fmt-check: ## 检查 Go 格式(有未格式化文件则失败)
	@out=$$(gofmt -l $(GOFMT_DIRS)); \
	if [ -n "$$out" ]; then echo "以下文件需要 gofmt:"; echo "$$out"; exit 1; fi
vet: ## go vet
	$(OFFLINE) $(GO) vet $(GOPKGS)
check: fmt-check vet test-go ## 总检查(不改写)

# ---- 镜像 ----
docker-build: ## 构建多架构镜像(linux/amd64 + linux/arm64)→ $(IMG)
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMG) .

# ---- kind 本地端到端 ----
kind-up: ## 创建 kind 集群(不存在才创建)
	kind get clusters 2>/dev/null | grep -qx "$(KIND_NAME)" || kind create cluster \
		--name "$(KIND_NAME)" --config deploy/kind/kind-config.yaml

kind-down: ## 删除 kind 集群
	kind delete cluster --name "$(KIND_NAME)"

kind-load: docker-build ## 构建镜像并装入 kind 集群
	kind load docker-image $(IMG) --name "$(KIND_NAME)"

deploy: ## 把 readlist 部署到 kind(先 load 镜像,再 apply 清单)
	kind load docker-image $(IMG) --name "$(KIND_NAME)"
	kubectl --context kind-$(KIND_NAME) apply -k deploy/kind
	kubectl --context kind-$(KIND_NAME) -n readlist rollout status deploy/readlist --timeout=180s

deploy-clean: ## 卸载 kind 上的 readlist
	kubectl --context kind-$(KIND_NAME) delete -k deploy/kind --ignore-not-found=true

e2e: ## 完整本地端到端:kind-up + deploy + 断言 API(推荐入口)
	./scripts/e2e-kind.sh

e2e-serve: ## 用 NodePort 地址打开浏览器(取决于 e2e 已部署)
	@echo "http://localhost:$(shell kubectl --context kind-$(KIND_NAME) -n readlist get svc readlist -o jsonpath='{.spec.ports[0].nodePort}')"

smoke: build ## 本地直跑 seed + score + dryrun(不经过 k8s)
	rm -f readlist.db readlist.db-wal readlist.db-shm
	./$(BIN) seed && ./$(BIN) score && ./$(BIN) dryrun

clean: ## 清理构建产物
	rm -rf bin
