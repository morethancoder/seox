# seox: one-word targets, `help` first; anything longer than a few lines lives
# in scripts/<verb>.sh. Document a target with `## description` after its name.

.DEFAULT_GOAL := help
.PHONY: help doctor build install test live lint fmt clean

help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "; printf "\n\033[1mUsage:\033[0m make \033[36m<target>\033[0m\n\n\033[1mTargets:\033[0m\n"} \
	     /^[a-z]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2} \
	     END {print ""}' $(MAKEFILE_LIST)

doctor: ## check go, then run `seox doctor`
	@bash scripts/doctor.sh

build: ## build ./seox (VERSION=v1.2.3 to stamp a version)
	@bash scripts/build.sh

install: ## go install seox into GOBIN
	@bash scripts/build.sh install

test: ## gofmt check, go vet and the offline Go tests (RACE=1 for -race)
	@bash scripts/test.sh

live: ## the live tests (real network; tests tagged live, named TestLive*)
	@bash scripts/test.sh live

lint: ## gofmt check and go vet only
	@bash scripts/test.sh lint

fmt: ## gofmt every Go file
	@gofmt -w .

clean: ## remove the built binary, dist/ and test caches
	@bash scripts/clean.sh
