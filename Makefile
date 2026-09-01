# Meeting Transcriber — the whole thing, in one place.
#
# `make install` sets up both halves so that logging in is the only thing
# anybody has to do:
#
#   the app        transcribes, diarizes, summarises, and serves the web UI
#   the listener   hears meetings happening and hands them to the app
#
# They are separate programs on purpose: the listener is opt-in, and the app
# behaves exactly as it always has without it.

PORT    ?= 8010
PROJECT := $(shell pwd)
UV      := $(shell command -v uv 2>/dev/null || echo /opt/homebrew/bin/uv)
AGENTS  := $(HOME)/Library/LaunchAgents
SERVER  := $(AGENTS)/com.dmykolen.meeting-transcriber.plist

.PHONY: install uninstall status logs test open cert

## install — set both halves to start at login, and start them now
install: install-app install-listener
	@echo
	@echo "  Meeting Transcriber is running."
	@echo "  Open        http://127.0.0.1:$(PORT)"
	@echo "  Listener    look for the ○ in the menu bar"
	@echo
	@echo "  The first launch asks for the microphone and for system audio"
	@echo "  recording. Both must be granted to mtd.app."
	@echo
	@$(MAKE) --no-print-directory status

install-app:
	@mkdir -p $(AGENTS)
	@sed -e 's|__UV__|$(UV)|' -e 's|__PORT__|$(PORT)|' -e 's|__PROJECT__|$(PROJECT)|' \
	     -e 's|__PATH__|$(PATH)|' -e 's|__HOME__|$(HOME)|' \
	     packaging/darwin/Server.plist > $(SERVER)
	@-launchctl unload $(SERVER) 2>/dev/null
	@launchctl load $(SERVER)
	@printf "waiting for the app"; \
	for i in $$(seq 1 60); do \
	  curl -sf http://127.0.0.1:$(PORT)/health >/dev/null 2>&1 && { echo " — up"; break; }; \
	  printf "."; sleep 1; \
	done
	@grep -q '^MT_LLM_API_KEY=.' .env 2>/dev/null || \
	  echo "  note: MT_LLM_API_KEY is not set in .env, so summaries and semantic search will not work. launchd does not see your shell's environment."

install-listener:
	@$(MAKE) --no-print-directory -C daemon install PORT=$(PORT)

## cert — stop macOS asking for the microphone after every rebuild
cert:
	@$(MAKE) --no-print-directory -C daemon cert

## uninstall — stop both and remove them from login. Recordings are kept.
uninstall:
	-launchctl unload $(SERVER) 2>/dev/null
	rm -f $(SERVER)
	@$(MAKE) --no-print-directory -C daemon uninstall
	@echo "removed. transcripts and recordings are untouched."

## status — is everything up?
status:
	@printf "  app       "; \
	  curl -sf http://127.0.0.1:$(PORT)/health >/dev/null 2>&1 \
	    && echo "running on http://127.0.0.1:$(PORT)" || echo "not running"
	@printf "  listener  "; \
	  pgrep -f 'mtd.app/Contents/MacOS/mtd' >/dev/null \
	    && echo "listening" || echo "not running"
	@printf "  waiting   "; \
	  ls "$(HOME)/Library/Application Support/mtd/spool"/*.wav 2>/dev/null | wc -l | tr -d ' ' \
	  | xargs -I{} echo "{} recordings still to send"

## logs — follow both
logs:
	tail -f /tmp/meeting-transcriber.log /tmp/mtd.log

## open — the web UI
open:
	@open http://127.0.0.1:$(PORT)

## test — both suites
test:
	uv run pytest -q
	@$(MAKE) --no-print-directory -C daemon test
