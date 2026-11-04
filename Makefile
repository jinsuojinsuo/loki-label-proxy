VER ?= `git show -s --format=%cd-%h --date=format:%y%m%d`
INSTALL_DIR ?= /usr/local/loki-label-proxy

help: ## Displays help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-z0-9A-Z_-]+:.*?##/ { printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.PHONY: help build test docker push install uninstall purge
build: test ## Build binaries with version set
	@CGO_ENABLED=0 go build -ldflags "-w -s \
	-X github.com/prometheus/common/version.Version=${VER} \
	-X github.com/prometheus/common/version.Revision=`git rev-parse --short HEAD` \
	-X github.com/prometheus/common/version.Branch=`git rev-parse --abbrev-ref HEAD` \
	-X github.com/prometheus/common/version.BuildUser=${USER}@`hostname` \
	-X github.com/prometheus/common/version.BuildDate=`date +%Y/%m/%d-%H:%M:%SZ`"

test: ## Run tests
	@go vet ./...
	@go test ./...

docker: ## Build docker image
	@docker build --platform linux/amd64 -t loki-label-proxy .

push: ## Push docker image
	docker tag loki-label-proxy sepa/loki-label-proxy
	docker push sepa/loki-label-proxy

install: build ## 安装程序、示例配置和 systemd 服务
	install -d "$(INSTALL_DIR)/config" "$(INSTALL_DIR)/logs" /etc/systemd/system
	install -m 755 loki-label-proxy "$(INSTALL_DIR)/loki-label-proxy"
	test -e "$(INSTALL_DIR)/start.sh" || install -m 755 start.example.sh "$(INSTALL_DIR)/start.sh"
	test -e "$(INSTALL_DIR)/config/loki-label-proxy.yaml" || install -m 644 loki-label-proxy.example.yaml "$(INSTALL_DIR)/config/loki-label-proxy.yaml"
	install -m 644 loki-label-proxy.service /etc/systemd/system/loki-label-proxy.service
	sed -i 's|/usr/local/loki-label-proxy|$(INSTALL_DIR)|g' /etc/systemd/system/loki-label-proxy.service
	systemctl daemon-reload
	@echo "安装完成。请先修改启动脚本及各实例的权限配置，再执行：systemctl start loki-label-proxy"

uninstall: ## 卸载程序和服务，保留修改过的文件及全部日志
	@if [ -f /etc/systemd/system/loki-label-proxy.service ]; then \
		systemctl disable --now loki-label-proxy; \
	fi
	rm -f /etc/systemd/system/loki-label-proxy.service "$(INSTALL_DIR)/loki-label-proxy"
	@if cmp -s start.example.sh "$(INSTALL_DIR)/start.sh"; then \
		rm -f "$(INSTALL_DIR)/start.sh"; \
	fi
	@if cmp -s loki-label-proxy.example.yaml "$(INSTALL_DIR)/config/loki-label-proxy.yaml"; then \
		rm -f "$(INSTALL_DIR)/config/loki-label-proxy.yaml"; \
	fi
	systemctl daemon-reload
	@echo "卸载完成。已保留修改过的启动脚本、配置，以及全部日志和自建配置。"

purge: ## 彻底卸载，删除全部配置和日志
	@test -n "$(INSTALL_DIR)" && test "$(INSTALL_DIR)" != "/" || { echo "INSTALL_DIR 不能为空或根目录。" >&2; exit 1; }
	@if [ -f /etc/systemd/system/loki-label-proxy.service ]; then \
		systemctl disable --now loki-label-proxy; \
	fi
	rm -f /etc/systemd/system/loki-label-proxy.service
	rm -rf -- "$(INSTALL_DIR)"
	systemctl daemon-reload
	@echo "已彻底卸载，安装目录中的程序、脚本、配置和日志已全部删除。"
