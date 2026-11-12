//go:build !opencodezen_utls

package opencodezen

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoUTLSTagConfigError verifies that a build without the opencodezen_utls
// tag fails the gated free-tier lane with a typed config error naming the
// build tag, while ungated operator overrides still work.
func TestNoUTLSTagConfigError(t *testing.T) {
	gateFile := t.TempDir() + "/gate.txt"
	require.NoError(t, writeFile(gateFile, []byte(gateBlock)))
	t.Setenv(gateFileEnvVar, gateFile)

	// Gated lane: default base URL + provisioned gate, no uTLS compiled in.
	gated := New(providers.ProviderConfig{APIKey: "k"}, providertest.Options(llmclient.Hooks{}))
	_, err := gated.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "m",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencodezen_utls build tag")

	// Ungated override: mock base URL works on any build.
	ungated := New(providers.ProviderConfig{APIKey: "k", BaseURL: "http://127.0.0.1:1"}, providertest.Options(llmclient.Hooks{}))
	p, ok := ungated.(*Provider)
	require.True(t, ok)
	assert.False(t, p.gated)
}
