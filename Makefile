# microchat —— 一个二进制：起来就**既对外服务、又在终端里直接聊**（`./microchat`）。
# 别的从简（用户 2026-09-29 定：别复杂化）：测试 / vet / 清理。
BIN := microchat

.PHONY: all build test vet run clean

all: build

build:
	cd src && go build -o ../$(BIN) .

test:
	cd src && go test ./...

vet:
	cd src && go vet ./...

run: build
	./$(BIN)

clean:
	rm -f $(BIN)
