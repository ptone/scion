.PHONY: build clean dev test web seq seq-web seq-dev seq-test seq-run seq-demo seq-demo-data

# Build the web frontend and Go binary
build: web
	go build -o agent-viz ./cmd/agent-viz/

# Build web frontend only
web:
	cd web && npm install && npx vite build

# Run in dev mode (requires Go backend running separately)
dev:
	cd web && npx vite

# Run Go tests
test:
	go test ./...

# Clean build artifacts
clean:
	rm -f agent-viz seq-viz
	rm -rf internal/server/dist/
	mkdir -p internal/server/dist && echo "placeholder" > internal/server/dist/.gitkeep
	rm -rf internal/seqserver/dist/
	mkdir -p internal/seqserver/dist && touch internal/seqserver/dist/.gitkeep

# ---------------------------------------------------------------------------
# seq-viz: the sequence visualizer (vertical metric-time sequence diagram).
# Separate frontend under web-seq/, separate binary under cmd/seq-viz/.
# ---------------------------------------------------------------------------

# Build the seq-viz frontend and Go binary
seq: seq-web
	go build -o seq-viz ./cmd/seq-viz/

# Build the seq-viz frontend only (emits into internal/seqserver/dist)
seq-web:
	cd web-seq && npm install && npm run build

# Run the seq-viz frontend dev server on :3100, proxying /api to :8090.
# Run `go run ./cmd/seq-viz --dev` alongside this.
seq-dev:
	cd web-seq && npm run dev

# Typecheck and unit-test the seq-viz frontend
seq-test:
	cd web-seq && npm run typecheck && npm test
	go test ./internal/digest/ ./internal/seqserver/

# Serve a synthetic run without needing real logs
seq-run: seq
	./seq-viz --synth-agents 60

# Serve the committed demo sample (small; see demo/README.md)
seq-demo: seq
	./seq-viz --digest-file demo/sample/run.digest.json

# Regenerate every demo dataset, including the committed sample
seq-demo-data:
	./demo/regenerate.sh
