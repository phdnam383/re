NS   ?= ifm
K8S  ?= deploy/k8s
K    := kubectl -n $(NS)
BIN  := re

.PHONY: deploy

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

build: tidy
	go build -o $(BIN) ./cmd/engine/

deploy-engine:
	$(K) apply -f $(K8S)/30-engine.yaml

undeploy-engine:
	$(K) delete -f $(K8S)/30-engine.yaml

logs-engine:
	@$(K) logs -f deploy/rule-engine -c engine

status:
	@$(K) get pods,svc
