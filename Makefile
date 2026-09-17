SHELL := /bin/bash
.PHONY: proto build test docker dev run-air migrate-up migrate-down migrate-version migrate-force create-server delete-server list-servers shell-server shell-incus enroll-server link-server restart-incus

proto:
	protoc --go_out=. --go_opt=module=github.com/arturo/autohost-cloud-api \
		--go-grpc_out=. --go-grpc_opt=module=github.com/arturo/autohost-cloud-api \
		proto/node_agent/v1/node_agent.proto

build:
	go build -o bin/autohost-cloud-lite ./cmd/api

test:
	go test ./...

docker:
	docker build -t autohost-cloud-api .

dev: 
	-kill -9 $$(lsof -t -i :9090) 2>/dev/null || true
	$(MAKE) run-air

run-air:
	docker compose up -d
	$(shell go env GOPATH)/bin/air -c .air.toml


migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

migrate-version:
	go run ./cmd/migrate version

migrate-force:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make migrate-force VERSION=<number>"; exit 1; fi
	go run ./cmd/migrate force $(VERSION)

# ─── Dynamic Test Servers ───────────────────────────────────────────────────
INSTANCE ?= autohost-test
NAME     ?= $(INSTANCE)
RAM      ?= 512MiB
CPU      ?= 1

create-server:
	@echo "🚀 Creando servidor '$(NAME)' con $(RAM) de RAM y $(CPU) CPU..."
	@if incus info $(NAME) >/dev/null 2>&1; then \
		echo "✅ La instancia '$(NAME)' ya existe"; \
	else \
		incus launch images:ubuntu/24.04 $(NAME) -c limits.memory=$(RAM) -c limits.cpu=$(CPU) -c security.nesting=true; \
		echo "⏳ Esperando arranque..."; \
		sleep 4; \
		echo "📦 Instalando Docker y herramientas en $(NAME)..."; \
		incus exec $(NAME) -- apt-get update -qq; \
		incus exec $(NAME) -- apt-get install -y -qq docker.io curl wireguard-tools; \
		echo "✅ Servidor '$(NAME)' listo."; \
	fi

delete-server:
	@echo "🗑️  Eliminando servidor '$(NAME)'..."
	-incus stop $(NAME) --force 2>/dev/null || true
	-incus delete $(NAME) 2>/dev/null || true
	@echo "✅ Servidor '$(NAME)' eliminado."

list-servers:
	@incus list

shell-server:
	@INSTANCES=($$(incus list -c n --format csv)); \
	if [ $${#INSTANCES[@]} -eq 0 ]; then \
		echo "❌ No hay instancias Incus disponibles."; \
		exit 1; \
	elif [ "$$NAME" != "autohost-test" ] && [ -n "$$NAME" ] && incus info "$$NAME" >/dev/null 2>&1; then \
		echo "🚀 Conectando a '$$NAME'..."; \
		incus exec "$$NAME" -- bash; \
	elif [ $${#INSTANCES[@]} -eq 1 ]; then \
		echo "🚀 Conectando a $${INSTANCES[0]}..."; \
		incus exec "$${INSTANCES[0]}" -- bash; \
	else \
		echo "📋 Servidores Incus disponibles:"; \
		echo ""; \
		for i in "$${!INSTANCES[@]}"; do \
			STATUS=$$(incus list "$${INSTANCES[$$i]}" -c s --format csv); \
			IPV4=$$(incus list "$${INSTANCES[$$i]}" -c 4 --format csv | head -n1); \
			printf "  [\033[1;32m%d\033[0m] %-20s (\033[1;34m%s\033[0m, %s)\n" "$$((i+1))" "$${INSTANCES[$$i]}" "$$STATUS" "$$IPV4"; \
		done; \
		echo ""; \
		printf "👉 Selecciona el número de servidor [1-%d]: " "$${#INSTANCES[@]}"; \
		read -r CHOICE; \
		if ! [[ "$$CHOICE" =~ ^[0-9]+$$ ]] || [ "$$CHOICE" -lt 1 ] || [ "$$CHOICE" -gt "$${#INSTANCES[@]}" ]; then \
			echo "❌ Selección inválida"; \
			exit 1; \
		fi; \
		SELECTED="$${INSTANCES[$$((CHOICE-1))]}"; \
		echo "🚀 Conectando a $$SELECTED..."; \
		incus exec "$$SELECTED" -- bash; \
	fi

shell-incus: shell-server

restart-incus:
	@echo "🔄 Reiniciando autohost-agent en '$(NAME)'..."
	incus exec $(NAME) -- sudo systemctl restart autohost-agent
	@echo "✅ Servicio reiniciado en '$(NAME)'."

update-incus:
	@(cd ../autohost-agent && $(MAKE) update-incus NAME=$(NAME))


deploy-incus:
	@echo "🚀 Desplegando autohost-agent en '$(NAME)'..."
	@incus info $(NAME) >/dev/null 2>&1 || $(MAKE) create-server NAME=$(NAME)
	(cd ../autohost-agent && go build -o autohost-agent cmd/agent/main.go)
	incus exec $(NAME) -- sudo id -u autohost >/dev/null 2>&1 || incus exec $(NAME) -- sudo useradd --system --no-create-home --shell /usr/sbin/nologin autohost
	incus exec $(NAME) -- sudo usermod -aG docker autohost 2>/dev/null || true
	incus exec $(NAME) -- sudo mkdir -p /etc/autohost /var/lib/autohost
	incus exec $(NAME) -- sudo chown autohost:autohost /var/lib/autohost
	incus file push ../autohost-agent/autohost-agent $(NAME)/usr/local/bin/autohost-agent --mode=0755
	incus file push ../autohost-agent/configs/agent.yaml $(NAME)/etc/autohost/config.yaml --mode=0640
	incus exec $(NAME) -- sudo chown root:autohost /etc/autohost/config.yaml
	incus file push ../autohost-agent/autohost-agent.service $(NAME)/etc/systemd/system/autohost-agent.service --mode=0644
	incus exec $(NAME) -- sudo systemctl daemon-reload
	@echo "✅ Agente instalado en '$(NAME)'."

enroll-server: deploy-incus
	@echo "🔗 Enrolando servidor '$(NAME)' en AutoHost..."
	@GATEWAY=$$(incus exec $(NAME) -- sh -c "ip route show default" | awk '/default/{print $$3}' | head -1); \
	 echo "   Host Gateway: $$GATEWAY"; \
	 echo "🔨 Compilando autohost-cli..."; \
	 (cd ../autohost-cli && go build -o dist/autohost main.go); \
	 incus file push ../autohost-cli/dist/autohost $(NAME)/usr/local/bin/autohost --mode=0755; \
	 echo "🚀 Iniciando asistente 'autohost up'..."; \
	 incus exec $(NAME) -- env AUTOHOST_CLOUD_URL="http://localhost:3000" AUTOHOST_API_URL="http://$$GATEWAY:8080" autohost up

link-server: deploy-incus
	@if [ -z "$(TOKEN)" ]; then echo "❌ Error: Especifica el token con TOKEN=<tu_token>"; exit 1; fi
	@GATEWAY=$$(incus exec $(NAME) -- sh -c "ip route show default" | awk '/default/{print $$3}' | head -1); \
	 echo "   Host Gateway: $$GATEWAY"; \
	 echo "🔨 Compilando autohost-cli..."; \
	 (cd ../autohost-cli && go build -o dist/autohost main.go); \
	 incus file push ../autohost-cli/dist/autohost $(NAME)/usr/local/bin/autohost --mode=0755; \
	 echo "🚀 Vinculando nodo con token..."; \
	 incus exec $(NAME) -- autohost enroll link --api http://$$GATEWAY:8080 --token $(TOKEN) --name $(NAME); \
	 incus exec $(NAME) -- sudo systemctl restart autohost-agent; \
	 echo "✅ Servidor '$(NAME)' vinculado y activo en AutoHost."
