# makefile for net-merge decky plugin

.PHONY: all build-backend build-frontend package deploy clean

DECK_IP ?= steamdeck.local
DECK_PORT ?= 22
DECK_USER ?= deck
PLUGIN_NAME = net-merge-decky
DEST = /home/$(DECK_USER)/homebrew/plugins/$(PLUGIN_NAME)

all: build-backend build-frontend package

build-backend:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/net-merge ./cmd/net-merge

build-frontend:
	pnpm run build

package:
	rm -rf out/$(PLUGIN_NAME)
	mkdir -p out/$(PLUGIN_NAME)/dist out/$(PLUGIN_NAME)/bin
	cp plugin.json package.json main.py LICENSE README.md THIRD-PARTY-NOTICES.md out/$(PLUGIN_NAME)/
	cp dist/index.js out/$(PLUGIN_NAME)/dist/
	cp bin/net-merge out/$(PLUGIN_NAME)/bin/
	chmod +x out/$(PLUGIN_NAME)/bin/net-merge
	cd out && zip -r $(PLUGIN_NAME).zip $(PLUGIN_NAME)
	@echo "plugin packaged at out/$(PLUGIN_NAME).zip"

deploy: package
	@echo "deploying to steam deck at $(DECK_USER)@$(DECK_IP):$(DECK_PORT)..."
	rsync -rtvpz --delete --no-owner --no-group -e "ssh -p $(DECK_PORT)" out/$(PLUGIN_NAME)/ $(DECK_USER)@$(DECK_IP):/tmp/$(PLUGIN_NAME)/
	ssh -p $(DECK_PORT) -t $(DECK_USER)@$(DECK_IP) "sudo rm -rf $(DEST) && sudo cp -r /tmp/$(PLUGIN_NAME) $(DEST) && sudo chmod +x $(DEST)/bin/net-merge && sudo systemctl restart plugin_loader"
	@echo "deployed and plugin_loader restarted"

clean:
	rm -rf bin dist out
