package opencodezen

import (
	"fmt"
	"os"

	"github.com/enterpilot/gomodel/internal/core"
)

// gateFileEnvVar names the file holding the captured OpenCode gate-prompt
// block. The block is data, not code: it lives in a file (never compiled
// in) so an operator can refresh it without a rebuild.
const gateFileEnvVar = "OPENCODEZEN_GATE_FILE"

// minGateBytes is the smallest gate-prompt capture the upstream accepts.
// Measured on 2026-11-11: a 1250-byte prefix of the captured block passed,
// a 1200-byte one did not. A shorter file therefore means a truncated or
// stale capture and must fail at boot, not silently 429 forever.
const minGateBytes = 1250

// gate carries the captured gate-prompt block. An empty block disables the
// injection (operator override to a mock or mirror base URL).
type gate struct {
	block string
}

// loadGate reads and validates the gate-prompt file named by envVar. An
// unset env var yields an empty gate (disabled); a present but unreadable
// or too-short file fails the boot with a clear error.
func loadGate(envVar string) (*gate, error) {
	path, ok := os.LookupEnv(envVar)
	if !ok || path == "" {
		return &gate{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("opencodezen: read gate file %s: %w", path, err)
	}
	if len(data) < minGateBytes {
		return nil, fmt.Errorf("opencodezen: gate file %s is %d bytes, need at least %d: truncated or stale capture", path, len(data), minGateBytes)
	}
	return &gate{block: string(data)}, nil
}

// injectGatePrompt merges the captured gate block into the system message.
// The gate content-matches the block, so it must stay byte-identical; the
// caller's own instructions are kept after it.
func (g *gate) injectGatePrompt(msgs []core.Message) []core.Message {
	if g == nil || g.block == "" {
		return msgs
	}
	out := make([]core.Message, 0, len(msgs)+1)
	merged := false
	for _, m := range msgs {
		if m.Role == "system" && !merged {
			if content, ok := m.Content.(string); ok {
				m.Content = g.block + "\n\n" + content
				merged = true
			}
		}
		out = append(out, m)
	}
	if !merged {
		out = append([]core.Message{{Role: "system", Content: g.block}}, out...)
	}
	return out
}
