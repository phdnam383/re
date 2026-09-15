NS   ?= ifm
K8S  ?= deploy/k8s
K    := kubectl -n $(NS)
BIN  := re
MSG  = context snapshot built
TAIL ?= 100
FOLLOW = false
export MSG TAIL FOLLOW

.PHONY: deploy logs-engine logs-engine-json

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

logs-engine-json: ## Pretty JSON logs: MSG='context snapshot built' TAIL=100 FOLLOW=false (requires jq)
	@command -v jq >/dev/null 2>&1 || { echo "jq is required for logs-engine-json" >&2; exit 1; }
	@case "$$TAIL" in -1) ;; ''|*[!0-9]*) echo 'TAIL must be a nonnegative integer or -1' >&2; exit 1;; esac; \
	case "$$FOLLOW" in true|false) ;; *) echo 'FOLLOW must be true or false' >&2; exit 1;; esac; \
	filter='(. as $$raw | try fromjson catch $$raw) | select($$msg == "" or (type == "object" and .msg == $$msg))'; \
	$(K) logs --tail=-1 deploy/rule-engine -c engine | \
	  jq -Rc --arg msg "$$MSG" "$$filter" | \
	  { if [ "$$TAIL" = -1 ]; then cat; else tail -n "$$TAIL"; fi; } | jq -r .; \
	if [ "$$FOLLOW" = true ]; then \
	  $(K) logs --follow=true --tail=0 deploy/rule-engine -c engine | \
	    jq --unbuffered -Rr --arg msg "$$MSG" "$$filter"; \
	fi

status:
	@$(K) get pods,svc
