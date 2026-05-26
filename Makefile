# Default target
.DEFAULT_GOAL := help

# ==============================================================================
# Run & Development
# ==============================================================================
.PHONY: run
run: ## Build frontend and run server
	@echo "--- Building frontend... ---"
	cd web && npm install && npm run build
	@echo "--- Preparing backend... ---"
	@echo "--- Starting backend... ---"
	go run ./main.go

.PHONY: dev
dev: ## Run in development mode (with race detection)
	@echo "🔧 Starting development mode..."
	go run -race ./main.go

.PHONY: dev-all
dev-all: ## Run backend (:3001) + frontend vite (:5173) together, Ctrl+C stops both
	@echo "🚀 dev-all: backend :3001  ·  frontend :5173 (vite HMR)"
	@[ -d web/node_modules ] || (echo "📦 first-time: npm install"; cd web && npm install)
	@trap 'echo ""; echo "🛑 stopping..."; kill 0' INT TERM EXIT; \
	  (cd web && npm run dev) & \
	  go run -race ./main.go; \
	  wait

# ==============================================================================
# Key Migration
# ==============================================================================
.PHONY: migrate-keys
migrate-keys: ## Execute key migration (usage: make migrate-keys ARGS="--from old --to new")
	@echo "🔑 Executing key migration..."
	@if [ -z "$(ARGS)" ]; then \
		echo "Usage:"; \
		echo "  Enable encryption: make migrate-keys ARGS=\"--to new-key\""; \
		echo "  Disable encryption: make migrate-keys ARGS=\"--from old-key\""; \
		echo "  Change key: make migrate-keys ARGS=\"--from old-key --to new-key\""; \
		echo ""; \
		echo "⚠️  Important: Always backup database before migration!"; \
		exit 1; \
	fi
	go run ./main.go migrate-keys $(ARGS)

# ==============================================================================
# FreeModels Drift Check
# ==============================================================================
.PHONY: check-freemodels-drift
check-freemodels-drift: ## Diff local FREE_PROVIDERS vs ofind registry (FAIL_ON_DRIFT=1 for CI)
	@go run ./main.go check-freemodels-drift $(if $(FAIL_ON_DRIFT),--fail-on-drift) $(if $(VERBOSE),-v)

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*?## "; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*?## / { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
