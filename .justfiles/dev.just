set working-directory := '..'

bridge:
    mkdir -p bin
    cd bridge && go build -o ../bin/obey-voice-engine .

build: bridge
    cargo build --locked

check:
    cargo fmt --all -- --check
    cargo clippy --locked --all-targets -- -D warnings
    cd bridge && go vet ./...

test:
    cargo test --locked
    cd bridge && go test -race ./...
