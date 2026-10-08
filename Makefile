# Copyright (c) 2025 AccelByte Inc. All Rights Reserved.
# This is licensed software from AccelByte Inc, for limitations
# and restrictions contact your company contract manager.

SHELL := /bin/bash

PROJECT_NAME := $(shell basename "$$(pwd)")
GOLANG_IMAGE := golang:1.24
PROTOC_IMAGE := proto-builder

IS_INSIDE_DEVCONTAINER := $(REMOTE_CONTAINERS)
BUILD_CACHE_VOLUME := $(shell echo '$(PROJECT_NAME)' | sed 's/[^a-zA-Z0-9_-]//g')-build-cache

build: prepare_build_cache
ifneq ($(IS_INSIDE_DEVCONTAINER),true)
	docker run -t --rm \
			-u $$(id -u):$$(id -g) \
			-e GOCACHE=/tmp/build-cache/go/cache \
			-e GOMODCACHE=/tmp/build-cache/go/modcache \
			-v $(BUILD_CACHE_VOLUME):/tmp/build-cache \
			-v $$(pwd):/data/ \
			-w /data/ \
			$(GOLANG_IMAGE) \
			go build -modcacherw	
else
	go build -modcacherw
endif

proto_image:
ifneq ($(IS_INSIDE_DEVCONTAINER),true)
	docker build --target proto-builder -t $(PROTOC_IMAGE) .
endif

proto: proto_image
ifneq ($(IS_INSIDE_DEVCONTAINER),true)
	docker run --tty --rm --user $$(id -u):$$(id -g) \
		--volume $$(pwd):/build \
		--workdir /build \
		--entrypoint /bin/bash \
		$(PROTOC_IMAGE) \
		proto.sh
else
	./proto.sh
endif

prepare_build_cache:
ifneq ($(IS_INSIDE_DEVCONTAINER),true)
	docker run -t --rm \
			-v $(BUILD_CACHE_VOLUME):/tmp/build-cache \
			busybox:1.37.0 \
			chown $$(id -u):$$(id -g) /tmp/build-cache		# Fix /tmp/build-cache folder owned by root
endif
