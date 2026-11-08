.PHONY: build test install
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ffnvr ./cmd/ffnvr
test:
	go vet ./... && go test ./...
install: build
	install -m 0755 ffnvr /usr/local/bin/ffnvr
	install -d /etc/ffnvr
	# the account of the recordings with /system/systemd security=high
	id ffnvr >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin ffnvr
