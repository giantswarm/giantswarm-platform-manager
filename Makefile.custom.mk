##@ Development

BINARY := giantswarm-platform-manager
CHART_DIR := helm/$(BINARY)

.PHONY: build-linux-amd64
build-linux-amd64: ## Build the linux/amd64 binary the Dockerfile expects.
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $(BINARY)-linux-amd64 .

.PHONY: docker-build
docker-build: build-linux-amd64 ## Build a local dev image (TAG=giantswarm-platform-manager:dev).
	docker build --build-arg TARGETOS=linux --build-arg TARGETARCH=amd64 -t $(or $(TAG),$(BINARY):dev) .

.PHONY: test-e2e
test-e2e: ## The identity-chain proofs against the fake GitHub (internal/e2e).
	go test -count=1 -v ./internal/e2e/...

MUSTER_TEST_FLAGS ?= --base-port 18000 --parallel 1 --readiness-timeout 60s --fail-fast

.PHONY: scenario-test
scenario-test: ## Run the muster scenarios in tests/scenarios (needs the muster binary on PATH).
	muster test --config tests/scenarios $(MUSTER_TEST_FLAGS)

##@ Helm

.PHONY: helm-lint
helm-lint: ## Lint the chart with the default, the oauth and the lab values.
	helm lint $(CHART_DIR)
	helm lint $(CHART_DIR) -f tests/test-values.yaml -f tests/oauth-values.yaml
	helm lint $(CHART_DIR) -f tests/test-values.yaml -f tests/lab-oauth-values.yaml

.PHONY: helm-template
helm-template: ## Render the chart with defaults.
	helm template $(BINARY) $(CHART_DIR)

.PHONY: helm-schema
helm-schema: ## Regenerate values.schema.json (needs helm-values-schema-json and schemalint on PATH; the pre-commit hook checks the result).
	helm-values-schema-json --config $(CHART_DIR)/.schema.yaml
	python3 -c 'import json,sys; h=lambda o: {**{k:v for k,v in o.items() if k!="additionalProperties"},"unevaluatedProperties":False} if ("$ref" in o and o.get("additionalProperties") is False) else o; p=sys.argv[1]; f=open(p,encoding="utf-8"); d=json.load(f,object_hook=h); f.close(); f=open(p,"w",encoding="utf-8"); json.dump(d,f); f.close()' $(CHART_DIR)/values.schema.json
	schemalint normalize $(CHART_DIR)/values.schema.json -o $(CHART_DIR)/values.schema.json --force
