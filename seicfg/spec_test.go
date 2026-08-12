//go:build configv2

package seicfg_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/spf13/viper"

	"github.com/sei-protocol/sei-chain/sei-cosmos/server"
	serverconfig "github.com/sei-protocol/sei-chain/sei-cosmos/server/config"
	"github.com/sei-protocol/sei-chain/sei-tendermint/config"
	"github.com/sei-protocol/sei-chain/seicfg"
)

// The acceptance gates for the second configuration implementation.
//
// Every gate here fails today, on purpose. They are written before the implementation so
// that the implementation has a target it cannot rationalise around, which is the same
// reason the characterization suite was written before this work started.
//
// Two gates are not in this file because they cannot be. Gate 5, that SEI_CONFIG_MANAGER=v2
// drives the new path end to end, needs the command tree and lives in
// cmd/seid/cmd/configmanager. Gate 6, that the legacy path is unchanged, is the existing
// characterization suite passing with zero test edits, which is a property of every commit
// rather than a test to add.
//
// A gate that passes for the wrong reason is worse than one that fails, so each holds both
// directions: the property, and that breaking the property is detected. Where only the
// forward direction is checked, a stub returning zeroes would satisfy it.

// ---------------------------------------------------------------------------------------
// Gate 1: each provider reads its own source and nothing else.
// ---------------------------------------------------------------------------------------

// TestGate1EachProviderReadsOnlyItsOwnSource sets one key in two sources and requires each
// provider to report only its own.
//
// This is the property the legacy path cannot state. Its layers are merged inside one viper
// before anything observes them, so there is no point at which a value's source is
// recoverable, and that is why precedence there cannot be reported to a node operator.
func TestGate1EachProviderReadsOnlyItsOwnSource(t *testing.T) {
	const key = "min-retain-blocks"

	dir := t.TempDir()
	appToml := filepath.Join(dir, "app.toml")
	if err := os.WriteFile(appToml, []byte("min-retain-blocks = 100000\n"), 0o600); err != nil {
		t.Fatalf("write app.toml: %v", err)
	}
	t.Setenv("SEID_MIN_RETAIN_BLOCKS", "200000")

	providers := map[string]seicfg.Provider{
		"file": seicfg.FileProvider(appToml),
		"env":  seicfg.EnvProvider("SEID"),
	}
	loaded := map[string]any{}
	for name, p := range providers {
		layer, err := p.Load()
		if err != nil {
			t.Fatalf("%s provider: Load: %v", name, err)
		}
		if layer.Source != name {
			t.Errorf("%s provider reported source %q. Source is what ties a layer to its entry in "+
				"Precedence, so a layer naming the wrong one is resolved at the wrong priority",
				name, layer.Source)
		}
		loaded[name] = layer.Values[key]
	}

	if loaded["file"] != int64(100000) && loaded["file"] != "100000" {
		t.Errorf("the file provider read %s as %#v, and app.toml sets it to 100000. A provider that "+
			"cannot report its own source's value has nothing for the resolver to reduce",
			key, loaded["file"])
	}
	if loaded["env"] != "200000" {
		t.Errorf("the env provider read %s as %#v, and the environment sets it to 200000",
			key, loaded["env"])
	}
	// The point of the gate. Two sources, two different answers, both still separable.
	if loaded["file"] == loaded["env"] {
		t.Errorf("both providers reported %#v for %s. They were given deliberately different values, "+
			"so agreement means one of them consulted the other's source. Once that happens a "+
			"diagnostic cannot tell a node operator which source won", loaded["file"], key)
	}
}

// TestGate1EveryProviderSourceIsDeclaredInPrecedence holds the two lists against each other.
//
// A provider whose source is absent from Precedence has no defined priority, and a resolver
// would have to invent one. An entry in Precedence with no provider is a layer that silently
// never contributes.
func TestGate1EveryProviderSourceIsDeclaredInPrecedence(t *testing.T) {
	all := []seicfg.Provider{
		seicfg.DefaultProvider(),
		seicfg.FileProvider("app.toml"),
		seicfg.EnvProvider("SEID"),
		seicfg.FlagProvider(nil),
	}

	declared := map[string]bool{}
	for _, s := range seicfg.Precedence {
		declared[s] = true
	}
	seen := map[string]bool{}
	for _, p := range all {
		if !declared[p.Source()] {
			t.Errorf("a provider reports source %q, which is not in Precedence %v. Its layer has no "+
				"defined priority, so the resolver would have to invent one", p.Source(), seicfg.Precedence)
		}
		seen[p.Source()] = true
	}
	for _, s := range seicfg.Precedence {
		if !seen[s] {
			t.Errorf("Precedence declares %q and no provider produces it, so that layer never "+
				"contributes and the declared order is describing something that does not run", s)
		}
	}
}

// ---------------------------------------------------------------------------------------
// Gate 2: precedence is declared and total.
// ---------------------------------------------------------------------------------------

// TestGate2ResolveIsIndependentOfLayerInputOrder is how "declared" is made falsifiable.
//
// If precedence comes from Precedence, shuffling the slice handed to Resolve changes
// nothing. If it comes from iteration order, or from a merge that lets the last writer win,
// this fails. The legacy path fails this by construction, because its answer depends on
// which viper instance a caller asked.
func TestGate2ResolveIsIndependentOfLayerInputOrder(t *testing.T) {
	layers := []seicfg.Layer{
		{Source: "default", Values: map[string]any{"pruning": "default", "min-retain-blocks": "0"}},
		{Source: "file", Values: map[string]any{"pruning": "custom", "min-retain-blocks": "100000"}},
		{Source: "env", Values: map[string]any{"min-retain-blocks": "200000"}},
	}

	forward, err := seicfg.Resolve(layers)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	reversed := append([]seicfg.Layer(nil), layers...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	backward, err := seicfg.Resolve(reversed)
	if err != nil {
		t.Fatalf("Resolve reversed: %v", err)
	}

	if !reflect.DeepEqual(forward, backward) {
		t.Fatalf("Resolve returned different Configs for the same layers in a different order, so "+
			"precedence is coming from iteration rather than from Precedence. That is the legacy "+
			"path's defect exactly: its answer depends on which instance a caller asked.\n"+
			"forward:  %#v\nbackward: %#v", forward, backward)
	}
}

// TestGate2HigherPrecedenceWins holds that the declared order is the order that runs.
//
// Asserted on a key three layers set, so the result distinguishes a resolver honouring the
// order from one taking the first or last layer it happened to see.
func TestGate2HigherPrecedenceWins(t *testing.T) {
	layers := []seicfg.Layer{
		{Source: "default", Values: map[string]any{"pruning": "default"}},
		{Source: "file", Values: map[string]any{"pruning": "nothing"}},
		{Source: "env", Values: map[string]any{"pruning": "custom"}},
	}

	got, err := seicfg.Resolve(layers)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// env sits above file sits above default in Precedence, so env's value is the answer.
	if got.App.Pruning != "custom" {
		t.Errorf("pruning resolved to %q where three layers set it and the highest-precedence one, "+
			"env, said custom. Precedence is %v, so this is what that order means in practice",
			got.App.Pruning, seicfg.Precedence)
	}
}

// ---------------------------------------------------------------------------------------
// Gate 3: the differential is exact.
// ---------------------------------------------------------------------------------------

// TestGate3ResolveAgreesWithTheLegacyPathOnEveryCoveredKey is the differential itself.
//
// Config is the comparison unit, which is why the flat view lives beside it. Two
// implementations that agreed on every value have to produce Configs that compare equal, and
// nothing about how a value was produced may appear in that comparison.
func TestGate3ResolveAgreesWithTheLegacyPathOnEveryCoveredKey(t *testing.T) {
	dir := t.TempDir()
	appToml := filepath.Join(dir, "app.toml")
	body := "min-retain-blocks = 100000\npruning = \"custom\"\ninter-block-cache = false\n"
	if err := os.WriteFile(appToml, []byte(body), 0o600); err != nil {
		t.Fatalf("write app.toml: %v", err)
	}

	// The legacy answer, through the reader the characterization suite pins.
	v := viper.New()
	v.Set("telemetry.global-labels", []any{})
	v.SetConfigFile(appToml)
	if err := v.MergeInConfig(); err != nil {
		t.Fatalf("legacy read: %v", err)
	}
	legacyApp, err := serverconfig.GetConfig(v)
	if err != nil {
		t.Fatalf("legacy GetConfig: %v", err)
	}
	legacy := seicfg.AdaptLegacy(legacyApp, config.DefaultConfig(), v).Config

	// The same configuration, through providers and Resolve.
	sources := []seicfg.Provider{seicfg.DefaultProvider(), seicfg.FileProvider(appToml)}
	layers := make([]seicfg.Layer, 0, len(sources))
	for _, p := range sources {
		layer, err := p.Load()
		if err != nil {
			t.Fatalf("%s provider: %v", p.Source(), err)
		}
		layers = append(layers, layer)
	}
	resolved, err := seicfg.Resolve(layers)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !reflect.DeepEqual(legacy.App, resolved.App) {
		t.Fatalf("the two implementations disagree on the app.toml surface for a configuration they "+
			"both read. Every difference here is a key a node would resolve differently after a "+
			"cutover.\nlegacy:   %#v\nresolved: %#v", legacy.App, resolved.App)
	}
}

// TestGate3DifferentialDetectsASingleBrokenLayer is the other direction, and it is the half
// that makes the gate above worth anything.
//
// A comparison that passes because both sides are zero would satisfy the forward assertion.
// Dropping one key from one layer has to produce a reported difference, or the differential
// is comparing a document against itself.
func TestGate3DifferentialDetectsASingleBrokenLayer(t *testing.T) {
	full := []seicfg.Layer{
		{Source: "default", Values: map[string]any{"pruning": "default", "inter-block-cache": true}},
		{Source: "file", Values: map[string]any{"pruning": "custom"}},
	}
	broken := []seicfg.Layer{
		{Source: "default", Values: map[string]any{"pruning": "default", "inter-block-cache": true}},
		{Source: "file", Values: map[string]any{}}, // the file layer lost its one key
	}

	a, err := seicfg.Resolve(full)
	if err != nil {
		t.Fatalf("Resolve full: %v", err)
	}
	b, err := seicfg.Resolve(broken)
	if err != nil {
		t.Fatalf("Resolve broken: %v", err)
	}

	if reflect.DeepEqual(a, b) {
		t.Fatal("dropping a key from a layer produced an identical Config, so the differential " +
			"cannot see a provider that stopped reading its source. A cutover would then be " +
			"green while the new implementation ignored a file entirely")
	}
}

// ---------------------------------------------------------------------------------------
// Gate 4: the view serves every key the boot asks for.
// ---------------------------------------------------------------------------------------

// recordingView wraps a FlatView and records every key asked of it.
//
// The key set is observed rather than hand-listed, which matters because 154 of the 481
// keys in this tree were findable only at runtime. A hand-written list would be a guess
// about the boot's behaviour, and the point of this gate is to stop guessing.
type recordingView struct {
	inner seicfg.FlatView
	asked map[string]bool
}

func (r *recordingView) Get(key string) any {
	r.asked[key] = true
	return r.inner.Get(key)
}

func (r *recordingView) keys() []string {
	out := make([]string, 0, len(r.asked))
	for k := range r.asked {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGate4ViewAnswersEveryKeyTheLegacyViperAnswers is the gate that decides whether a
// second implementation can boot at all.
//
// On day one all 153 read sites still index configuration by string, so an implementation
// with no viper serves them from a view projected off its Config. Any key the view cannot
// answer is a read that resolves to nil where the legacy path resolved a value.
func TestGate4ViewAnswersEveryKeyTheLegacyViperAnswers(t *testing.T) {
	legacyFlat := viper.New()
	legacyFlat.Set("telemetry.global-labels", []any{})
	legacyFlat.Set(server.FlagMinRetainBlocks, "100000")
	legacyFlat.Set(server.FlagInterBlockCache, true)
	legacyFlat.Set("pruning", "custom")
	legacyFlat.Set("state-store.ss-keep-recent", int64(400000))

	legacyApp, err := serverconfig.GetConfig(legacyFlat)
	if err != nil {
		t.Fatalf("legacy GetConfig: %v", err)
	}
	cfg := seicfg.AdaptLegacy(legacyApp, config.DefaultConfig(), legacyFlat).Config

	projected := &recordingView{inner: seicfg.View(cfg), asked: map[string]bool{}}

	var unanswered []string
	for _, key := range legacyFlat.AllKeys() {
		want := legacyFlat.Get(key)
		got := projected.Get(key)
		if !reflect.DeepEqual(want, got) {
			unanswered = append(unanswered, key)
			t.Errorf("%s resolves to %#v (%T) through the projected view and %#v (%T) through the "+
				"legacy viper. A read that has not migrated goes through this view, so a difference "+
				"here is a read that changes answer at the cutover", key, got, got, want, want)
		}
	}
	if len(unanswered) > 0 {
		t.Logf("%d of %d keys unanswered. Keys asked: %v",
			len(unanswered), len(legacyFlat.AllKeys()), projected.keys())
		t.Log("If a key cannot be served because Config has no field for it, that is the schema " +
			"gap: 109 app.toml keys have no field reachable from the reflected structs. Name those " +
			"keys explicitly rather than letting them resolve to nil.")
	}
}
