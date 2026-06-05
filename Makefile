# SchemaLock app — Go binary + release Makefile.
#
# Tag-and-push triggers .github/workflows/release.yml on GitHub, which
# builds 5 platform binaries + SHA256SUMS and creates the release. This
# Makefile wraps the preflight + tag + push and tails the workflow run.

SHELL  := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

VERSION ?=
TAG     := v$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help build test vet preflight tag release watch

help:
	@printf "SchemaLock app — release targets\n\n"
	@printf "  make build               go build -> bin/schemalock (host platform)\n"
	@printf "  make test                go test ./... -race\n"
	@printf "  make vet                 go vet ./...\n"
	@printf "  make tag VERSION=X.Y.Z   preflight + create + push v\$$VERSION tag\n"
	@printf "  make release VERSION=X.Y.Z   test + tag + watch workflow\n"
	@printf "  make watch               tail the most recent release workflow run\n"

build:
	go build -o bin/schemalock ./cmd/schemalock

test:
	go test ./... -race

vet:
	go vet ./...

preflight:
	@if [ -z "$(VERSION)" ]; then \
	  echo "ERROR: VERSION=X.Y.Z required (e.g. make tag VERSION=0.1.1)"; exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "ERROR: working tree not clean"; exit 1; \
	fi
	@if [ "$$(git rev-parse --abbrev-ref HEAD)" != "master" ]; then \
	  echo "ERROR: not on master branch"; exit 1; \
	fi
	@if git rev-parse "$(TAG)" >/dev/null 2>&1; then \
	  echo "ERROR: tag $(TAG) already exists"; exit 1; \
	fi
	@echo "preflight OK — $(TAG)"

tag: preflight
	git tag -a "$(TAG)" -m "$(TAG)"
	git push origin "$(TAG)"
	@echo ""
	@echo "==> tag pushed; release workflow will build + publish 5 binaries"
	@echo "    https://github.com/schemalock/app/actions/workflows/release.yml"

release: test tag watch

watch:
	@RUN_ID=$$(gh run list --workflow=release.yml --limit 1 --json databaseId --jq '.[0].databaseId'); \
	  if [ -z "$$RUN_ID" ] || [ "$$RUN_ID" = "null" ]; then \
	    echo "no recent release workflow run found"; exit 1; \
	  fi; \
	  gh run watch "$$RUN_ID" --exit-status --interval 15
