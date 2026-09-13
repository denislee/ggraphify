# Installing is what makes the board reachable from fuzzel: a launcher goes on
# PATH, the entry into the XDG applications directory, the icon into the
# hicolor theme.
#
# What lands on PATH is packaging/ggraphify-run, not the binary. It rebuilds
# from this checkout and then execs the result, so fuzzel always starts the
# current source instead of whatever was copied here by the last install. That
# makes `make install` a one-time setup step rather than something to repeat
# after every rebuild — only packaging changes need it again.

APPID   := dev.dns.ggraphify
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
LIBDIR  := $(PREFIX)/lib/ggraphify
APPDIR  := $(PREFIX)/share/applications
ICONDIR := $(PREFIX)/share/icons/hicolor/scalable/apps
GOFILES := $(shell find . -name '*.go' -not -path './.git/*')
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Baked into the launcher so it can find this checkout and a go toolchain from
# a session that has neither on PATH — fuzzel does not source your shell rc.
#
# It has to be the *same* go your shell uses, not merely any go: Go's build
# cache is keyed on the compiler's own version, so a different patch release
# shares nothing and rebuilds the whole cgo/GTK4 tree from cold. Here that is
# the difference between a launch costing ~0.1s and one costing minutes.
SRCDIR  := $(CURDIR)
GO      := $(shell command -v go)

.PHONY: all build run scan install uninstall test vet fmt clean pgo

all: build

build: ggraphify ggraphify-scan ggraphify-job

ggraphify: $(GOFILES) go.mod go.sum
	go build -ldflags "$(LDFLAGS)" -o $@ .

ggraphify-scan: $(GOFILES) go.mod go.sum
	go build -o $@ ./cmd/ggraphify-scan

ggraphify-job: $(GOFILES) go.mod go.sum
	go build -o $@ ./cmd/ggraphify-job

# The one target for "build it and look at it". It rebuilds only if a source
# file moved — Go's build cache keeps the expensive cgo/GTK4 objects, so an
# unchanged tree costs about a second and a one-package change not much more.
#
# GSK_RENDERER is pinned to vulkan exactly as packaging/ggraphify-run pins it,
# and for the same reason: `make run` and a fuzzel launch must render through
# the same path, or a frame-timing problem that only reproduces under one of
# them costs an afternoon to find. An explicit value in the environment still
# wins, so `GSK_RENDERER=cairo make run` works.
#
# Extra flags go through ARGS:
#
#     make run
#     make run ARGS="-roots ~/git -dark"
#     make run ARGS=-h
ARGS ?=

run: ggraphify
	GSK_RENDERER=$${GSK_RENDERER:-vulkan} ./ggraphify $(ARGS)

# The headless half, which needs no display and is the fastest way to see
# whether discovery and the derived state are right.
scan: ggraphify-scan
	./ggraphify-scan $(ARGS)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Profile-guided optimization.
#
# Go picks up a file named default.pgo beside the main package automatically —
# no build flag and no change to the targets above — so this target's whole job
# is to produce one. There is nothing useful to collect from a test: what the
# board spends its time on is a real corpus of repositories, real subprocesses
# and real GTK, so the profile has to come from a board somebody is using.
#
#     GGRAPHIFY_PPROF=127.0.0.1:6060 ./ggraphify &
#     make pgo
#
# Expect a few percent, not a transformation: much of this board's time is
# inside cgo and GTK, where the Go compiler has nothing to reach.
PGO_SECONDS ?= 60
PGO_ADDR    ?= 127.0.0.1:6060

pgo:
	@echo "collecting $(PGO_SECONDS)s of CPU profile from $(PGO_ADDR) — use the board while this runs"
	curl -fsS -o default.pgo "http://$(PGO_ADDR)/debug/pprof/profile?seconds=$(PGO_SECONDS)"
	@echo 'wrote default.pgo — go build picks it up automatically from here on'

install: build
	# A stripped binary for the installed copy. The launcher rebuilds from
	# source on every launch with a bare `go build`, so it always produces an
	# unstripped binary with full DWARF — a panic during development still has
	# a symbolised trace. Stripping only this seed copy saves several MB.
	install -d $(LIBDIR)
	$(GO) build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(LIBDIR)/ggraphify .
	$(GO) build -trimpath -ldflags "-s -w" -o $(LIBDIR)/ggraphify-scan ./cmd/ggraphify-scan
	$(GO) build -trimpath -ldflags "-s -w" -o $(LIBDIR)/ggraphify-job ./cmd/ggraphify-job
	install -d $(BINDIR)
	sed -e 's|@SRC@|$(SRCDIR)|' -e 's|@GO@|$(GO)|' -e 's|@LIBEXEC@|$(LIBDIR)/ggraphify|' \
		packaging/ggraphify-run > $(BINDIR)/ggraphify
	chmod 755 $(BINDIR)/ggraphify
	ln -sf $(LIBDIR)/ggraphify-scan $(BINDIR)/ggraphify-scan
	ln -sf $(LIBDIR)/ggraphify-job $(BINDIR)/ggraphify-job
	install -Dm644 packaging/$(APPID).svg $(ICONDIR)/$(APPID).svg
	install -d $(APPDIR)
	sed 's|@BIN@|$(BINDIR)/ggraphify|' packaging/$(APPID).desktop > $(APPDIR)/$(APPID).desktop
	chmod 644 $(APPDIR)/$(APPID).desktop
	-update-desktop-database $(APPDIR)
	-gtk4-update-icon-cache -qtf $(PREFIX)/share/icons/hicolor
	@echo "installed: $(BINDIR)/ggraphify — type 'ggraphify' into fuzzel"
	@echo "            it rebuilds from $(SRCDIR) on every launch"
	@echo "            set GGRAPHIFY_NO_REBUILD=1 to run the last build as-is"

uninstall:
	rm -f $(BINDIR)/ggraphify $(BINDIR)/ggraphify-scan $(BINDIR)/ggraphify-job
	rm -f $(APPDIR)/$(APPID).desktop $(ICONDIR)/$(APPID).svg
	rm -rf $(LIBDIR)
	-update-desktop-database $(APPDIR)
	-gtk4-update-icon-cache -qtf $(PREFIX)/share/icons/hicolor

clean:
	rm -f ggraphify ggraphify-scan ggraphify-job
