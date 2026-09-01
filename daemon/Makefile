# mtd — the always-on listener. See ../DAEMON.md.

# audiotee has no tagged releases and warns that its API is unstable, so the
# commit is pinned rather than tracked.
AUDIOTEE_REPO := https://github.com/makeusabrew/audiotee.git
AUDIOTEE_REF  := 56ac954369a09318e46b88a6eec33c2d2b0d32a3
SILERO        := https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx

BUILD   := build
APP     := $(BUILD)/mtd.app
VENDOR  := $(BUILD)/audiotee
RUNTIME := runtime
ORT     := $(shell go env GOMODCACHE)/github.com/yalue/onnxruntime_go@v1.35.0/test_data

UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
  ORT_LIB := onnxruntime.dylib
  ORT_SRC := $(ORT)/onnxruntime_arm64.dylib
else
  ORT_LIB := onnxruntime.so
  ORT_SRC := $(ORT)/onnxruntime_arm64.so
endif

.PHONY: all runtime test bundle probe run install uninstall clean cert

# A stable signing identity, if one has been made. Without it the bundle is
# ad-hoc signed, which works — but an ad-hoc signature contains a hash of the
# binary, macOS ties the microphone grant to that hash, and so every rebuild is
# a different application that has to ask again. `make cert` fixes that once.
# The fingerprint, not the name: a self-signed certificate is never "valid" for
# codesigning (that means trusted), so -v hides it, and two certificates with the
# same name make signing by name ambiguous. The hash is neither.
#
# Sorted, and then the first — because the keychain lists duplicates in no fixed
# order, and picking whichever came first would silently change the designated
# requirement between builds, which is the exact thing this is here to prevent.
# Caught by rebuilding twice and diffing the requirement.
IDENTITY := $(shell security find-identity -p codesigning 2>/dev/null | awk '/Meeting Transcriber Local/ {print $$2}' | sort | head -1)
SIGN     := $(if $(IDENTITY),$(IDENTITY),-)

## cert — create a local signing identity so permissions survive rebuilds
cert:
	@bash packaging/darwin/make-signing-identity.sh

all: $(BUILD)/mtd

test: runtime
	go vet ./...
	go test ./...

$(BUILD)/mtd: $(shell find . -name '*.go')
	go build -o $@ ./cmd/mtd

# --- the two files that cannot be compiled in -------------------------------

runtime: $(RUNTIME)/$(ORT_LIB) $(RUNTIME)/silero_vad.onnx

$(RUNTIME)/$(ORT_LIB):
	@mkdir -p $(RUNTIME)
	cp $(ORT_SRC) $@
	chmod u+w $@

$(RUNTIME)/silero_vad.onnx:
	@mkdir -p $(RUNTIME)
	curl -sSL -o $@ $(SILERO)

# --- macOS ------------------------------------------------------------------

$(VENDOR)/.build/release/audiotee:
	git clone --quiet $(AUDIOTEE_REPO) $(VENDOR) 2>/dev/null || true
	cd $(VENDOR) && git fetch --quiet origin $(AUDIOTEE_REF) && git checkout --quiet $(AUDIOTEE_REF)
	cd $(VENDOR) && swift build -c release

# The bundle is what macOS attaches the microphone and system-audio grants to,
# so the daemon is always run from inside it — never as a bare binary.
#
# Signing is ad-hoc and there is no notarization. That is a deliberate choice,
# and it is why the identifier in Info.plist must never change: the grant
# follows the signature, and a new identity means being asked again.
bundle: $(BUILD)/mtd runtime $(VENDOR)/.build/release/audiotee
	rm -rf $(APP)
	mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	cp packaging/darwin/Info.plist $(APP)/Contents/Info.plist
	cp $(BUILD)/mtd $(APP)/Contents/MacOS/mtd
	cp $(VENDOR)/.build/release/audiotee $(APP)/Contents/Resources/audiotee
	cp $(RUNTIME)/$(ORT_LIB) $(RUNTIME)/silero_vad.onnx $(APP)/Contents/Resources/
	codesign --force --sign $(SIGN) $(APP)/Contents/Resources/audiotee
	codesign --force --sign $(SIGN) $(APP)/Contents/Resources/$(ORT_LIB)
	codesign --force --sign $(SIGN) $(APP)/Contents/MacOS/mtd
	codesign --force --sign $(SIGN) $(APP)
	@codesign --verify $(APP)
	@if [ -n "$(IDENTITY)" ]; then \
	  echo "signed by $(IDENTITY) — the microphone permission survives rebuilds"; \
	else \
	  echo "signed ad-hoc — macOS will ask for the microphone again after each rebuild."; \
	  echo "run 'make cert' once to stop that."; \
	fi

# Launching through `open` is what makes macOS treat the bundle as the
# responsible application; run the binary straight from a shell and the grant is
# attributed to the terminal instead, which is how the first spike captured five
# seconds of pure silence.
probe: bundle
	@rm -f $(BUILD)/probe.out $(BUILD)/probe.err
	open -W --stdout $(PWD)/$(BUILD)/probe.out --stderr $(PWD)/$(BUILD)/probe.err \
		$(APP) --args -probe 8s
	@cat $(BUILD)/probe.out
	@echo "--- log ---"; tail -5 $(BUILD)/probe.err

run: bundle
	open $(APP)

# --- autostart ---------------------------------------------------------------

PLIST := $(HOME)/Library/LaunchAgents/com.dmykolen.mtd.plist

PORT ?= 8010

install: bundle
	@mkdir -p $(HOME)/Applications $(dir $(PLIST))
	@# point the listener at the app, without disturbing anything else in the config
	@CFG="$(HOME)/Library/Application Support/mtd/mtd.toml"; 	 if [ -f "$$CFG" ]; then 	   sed -i '' 's|^server = .*|server = "http://127.0.0.1:$(PORT)"|' "$$CFG"; 	 fi
	rm -rf $(HOME)/Applications/mtd.app
	cp -R $(APP) $(HOME)/Applications/mtd.app
	sed 's|__APP__|$(HOME)/Applications/mtd.app|' packaging/darwin/LaunchAgent.plist > $(PLIST)
	-launchctl unload $(PLIST) 2>/dev/null
	@# belt and braces: a copy started by hand would otherwise keep the lock
	-pkill -f 'mtd.app/Contents/MacOS/mtd' 2>/dev/null; sleep 1
	launchctl load $(PLIST)
	@echo
	@echo "installed to ~/Applications/mtd.app and set to start at login."
	@echo "the first run asks for the microphone and for system audio recording."

uninstall:
	-launchctl unload $(PLIST) 2>/dev/null
	rm -f $(PLIST)
	rm -rf $(HOME)/Applications/mtd.app
	@echo "removed. recordings already in the spool are left alone."

clean:
	rm -rf $(BUILD)
