.DEFAULT_GOAL := build
BUILD_DIR ?= $(CURDIR)/.build
SOURCE := $(CURDIR)/firefox-vpn/src
GO ?= go
GOARCH ?= amd64
GOARM ?= 7
GOMIPS ?= softfloat
GO_TAGS := netgo,osusergo,http2legacy
# Smaller Go 1.27 JSON implementation; keep builds and tests on the same path.
export GOEXPERIMENT := nojsonv2
PACKAGE_VERSION := $(shell sed -n 's/^PKG_VERSION:=//p' firefox-vpn/Makefile)-r$(shell sed -n 's/^PKG_RELEASE:=//p' firefox-vpn/Makefile)

.PHONY: build native-build test check crosscheck clean

build:
	mkdir -p dist
	cd $(SOURCE) && CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) GOARM=$(GOARM) GOMIPS=$(GOMIPS) $(GO) build -trimpath -tags $(GO_TAGS) -ldflags='-s -w -X firefox-vpn/internal/version.Current=$(PACKAGE_VERSION)' -o $(CURDIR)/dist/firefox-vpn-linux-$(GOARCH) ./cmd/firefox-vpn-openwrt

native-build:
	python3 scripts/build_local.py build --go "$(GO)" --binaries "$(BUILD_DIR)/native" $(if $(ARCHITECTURES),--architectures "$(ARCHITECTURES)")

test:
	cd $(SOURCE) && $(GO) test -race -tags $(GO_TAGS) ./...

check:
	node --check luci-app-firefox-vpn/htdocs/luci-static/resources/view/firefox-vpn/overview.js
	node tests/test_view.js
	sh -n firefox-vpn/files/firefox-vpn.init
	sh -n install.sh
	python3 -m unittest discover -s tests -v

crosscheck:
	$(MAKE) build GOARCH=arm64
	$(MAKE) build GOARCH=arm GOARM=7
	$(MAKE) build GOARCH=mips GOMIPS=softfloat
	$(MAKE) build GOARCH=mipsle GOMIPS=softfloat
	$(MAKE) build GOARCH=amd64

clean:
	rm -rf $(BUILD_DIR) dist
