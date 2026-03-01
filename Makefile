BINARY_SERVER = phantom-server
BINARY_CLIENT = phantom-client
BUILD_DIR = build
LDFLAGS = -s -w

STEALTH_SERVER = phantom-stealth-server
STEALTH_CLIENT = phantom-stealth-client

.PHONY: build clean release stealth certs ssh-keys

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_SERVER) ./cmd/server
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_CLIENT) ./cmd/client

clean:
	rm -rf $(BUILD_DIR)

# Cross-compile for 6 platforms.
release:
	@mkdir -p $(BUILD_DIR)
	@for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${pair%%/*}; arch=$${pair##*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "Building $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" \
			-o $(BUILD_DIR)/$(BINARY_SERVER)-$$os-$$arch$$ext ./cmd/server; \
		GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" \
			-o $(BUILD_DIR)/$(BINARY_CLIENT)-$$os-$$arch$$ext ./cmd/client; \
	done

STEALTH_GO ?= go1.25.0
STEALTH_GARBLE ?= garble

stealth:
	@mkdir -p $(BUILD_DIR)
	@for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${pair%%/*}; arch=$${pair##*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "Building stealth $$os/$$arch..."; \
		PATH=$$($(STEALTH_GO) env GOROOT)/bin:$$PATH \
		GOOS=$$os GOARCH=$$arch $(STEALTH_GARBLE) -literals -tiny build -trimpath -buildvcs=false \
			-tags stealth -ldflags "$(LDFLAGS)" \
			-o $(BUILD_DIR)/$(STEALTH_SERVER)-$$os-$$arch$$ext ./cmd/server; \
		PATH=$$($(STEALTH_GO) env GOROOT)/bin:$$PATH \
		GOOS=$$os GOARCH=$$arch $(STEALTH_GARBLE) -literals -tiny build -trimpath -buildvcs=false \
			-tags stealth -ldflags "$(LDFLAGS)" \
			-o $(BUILD_DIR)/$(STEALTH_CLIENT)-$$os-$$arch$$ext ./cmd/client; \
	done

# Generate self-signed TLS certificates for testing.
certs:
	@mkdir -p certs
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
		-keyout certs/server.key -out certs/server.crt \
		-days 365 -nodes -subj "/CN=phantom-proxy" \
		-addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
	@echo "Certificates generated in certs/"

# Generate SSH keys for testing.
ssh-keys:
	@mkdir -p keys
	ssh-keygen -t ed25519 -f keys/host_key -N "" -q
	ssh-keygen -t ed25519 -f keys/client_key -N "" -q
	@echo "SSH keys generated in keys/"
