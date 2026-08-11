package seicfg_test

import (
	"reflect"
	"testing"

	"github.com/spf13/cast"
	"github.com/spf13/viper"

	"github.com/sei-protocol/sei-chain/sei-cosmos/server"
	serverconfig "github.com/sei-protocol/sei-chain/sei-cosmos/server/config"
	servertypes "github.com/sei-protocol/sei-chain/sei-cosmos/server/types"
	"github.com/sei-protocol/sei-chain/sei-tendermint/config"
	"github.com/sei-protocol/sei-chain/seicfg"
)

// These hold the two properties the rest of the migration off the flat view depends on: that
// assembling a Resolved changes nothing a read can see, and that two resolvers producing the
// same values produce Configs that compare equal.
//
// The identity assertions are deliberately stronger than equality. A Resolved that rebuilt or
// copied the flat view would still satisfy an equality check on the keys a test happened to
// set, and would silently drop whatever the copy did not carry.

// newFlatView returns a viper carrying the shapes the legacy path actually produces: a string
// from env or quoted TOML, an int64 from an unquoted TOML integer, a float64 from a TOML float,
// and a nested key. The float is here because its Go type is what made one receipt read
// architecture dependent, so a view that normalised types is caught by that key first.
func newFlatView() *viper.Viper {
	v := viper.New()
	v.Set(server.FlagMinRetainBlocks, "100000")
	v.Set("state-store.ss-keep-recent", int64(400000))
	v.Set("evm.max_log_bytes", float64(1e19))
	v.Set(server.FlagMinGasPrices, "0.01usei")
	return v
}

// TestFlatIsTheValueItWasGiven pins that the flat view survives assembly untouched.
func TestFlatIsTheValueItWasGiven(t *testing.T) {
	flat := newFlatView()

	got := seicfg.AdaptLegacy(*serverconfig.DefaultConfig(), config.DefaultConfig(), flat)

	if got.Flat != seicfg.FlatView(flat) {
		t.Fatalf("Flat is %#v rather than the value AdaptLegacy was given. Every read that has not "+
			"moved onto a Config field goes through it, so a Resolved that rebuilds or copies it "+
			"resolves those reads against something the legacy handler never produced", got.Flat)
	}
}

// TestFlatResolvesEveryKeyTheSameWay holds the flat view against the value it wraps, key by key
// and type by type, so a read through Resolved cannot differ from the same read before it existed.
func TestFlatResolvesEveryKeyTheSameWay(t *testing.T) {
	flat := newFlatView()

	resolved := seicfg.AdaptLegacy(*serverconfig.DefaultConfig(), config.DefaultConfig(), flat)

	for _, key := range flat.AllKeys() {
		want, got := flat.Get(key), resolved.Flat.Get(key)
		if want != got {
			t.Errorf("%s resolves to %#v (%T) through Resolved and %#v (%T) directly. The two have to "+
				"agree for this type to be a change in shape rather than in behaviour, and a type "+
				"difference alone changes what a cast at the read site produces",
				key, got, got, want, want)
		}
	}
}

// TestFlatViewSatisfiesAppOptions pins the assignability the migration window rests on.
//
// seicfg declares FlatView rather than importing servertypes.AppOptions, because sei-cosmos's
// types package names a Resolved and the reverse import would be a cycle. Two interfaces with
// identical method sets are assignable, which is what lets an un-migrated reader keep its
// AppOptions parameter. If the two ever drift, every such reader stops compiling at once and
// this says why.
func TestFlatViewSatisfiesAppOptions(t *testing.T) {
	flat := newFlatView()

	var opts servertypes.AppOptions = seicfg.AdaptLegacy(
		*serverconfig.DefaultConfig(), config.DefaultConfig(), flat).Flat

	if opts.Get(server.FlagMinRetainBlocks) != flat.Get(server.FlagMinRetainBlocks) {
		t.Fatal("a FlatView used as an AppOptions resolves differently from the viper behind it")
	}
}

// TestConfigComparesEqualAcrossProducers is why Config and the flat view are separate types.
//
// The differential the v2 cutover rests on compares what two resolvers produced. Config is that
// comparison unit, so two resolvers that agreed on every value have to produce Configs that
// compare equal even where they built the flat view differently, or did not build one at all.
func TestConfigComparesEqualAcrossProducers(t *testing.T) {
	app, tm := *serverconfig.DefaultConfig(), config.DefaultConfig()

	// A legacy-shaped producer, carrying a viper.
	legacy := seicfg.AdaptLegacy(app, tm, newFlatView())
	// A producer that resolved the same values without building a flat view, which is what a
	// resolver that has stopped using viper looks like.
	direct := seicfg.AdaptLegacy(app, tm, nil)

	if !reflect.DeepEqual(legacy.Config, direct.Config) {
		t.Fatalf("two producers agreeing on every value produced Configs that differ. The "+
			"differential compares Configs, so a field recording which machinery a producer used "+
			"makes that comparison report a difference the node cannot see.\nlegacy: %#v\ndirect: %#v",
			legacy.Config, direct.Config)
	}
	// And the reverse, so the assertion above is not passing because Config carries nothing.
	changed := seicfg.AdaptLegacy(app, tm, nil)
	changed.Config.App.Pruning = "custom"
	if reflect.DeepEqual(legacy.Config, changed.Config) {
		t.Fatal("Configs differing in a resolved value compared equal, so the comparison above " +
			"would hold for two resolvers that disagreed on everything")
	}

	// Whether App holds a configuration at all is a difference the node can see, so it is part of
	// the compared value rather than machinery. This is the one intended inequality: it separates
	// a producer that resolved app.toml from one that never had it, which is not the same as two
	// resolvers disagreeing. Both sides of a v2 differential resolve app.toml, so both carry true.
	if reflect.DeepEqual(legacy.Config, seicfg.AdaptLegacyWithoutApp(nil).Config) {
		t.Fatal("a Config with a resolved app.toml surface compared equal to one without. The two " +
			"describe different nodes, so a differential holding them equal would pass a resolver " +
			"that had silently stopped reading app.toml at all")
	}
}

// TestInterBlockCacheStructFieldAgreesWithTheFlatRead clears the first read queued to move off
// the flat view, before it moves.
//
// newApp reads inter-block-cache as cast.ToBool(appOpts.Get(key)); GetConfig fills
// Config.App.InterBlockCache with v.GetBool of the same key. The two have to agree on every shape
// the layers can deliver or moving the read would change what a node does with an operator's
// value. The move itself waits on the rollback command having an app.toml surface, so this holds
// the equivalence in the meantime rather than discovering it later.
func TestInterBlockCacheStructFieldAgreesWithTheFlatRead(t *testing.T) {
	shapes := []any{nil, true, false, "true", "false", "1", "0", "yes", "on", "not-a-bool",
		int64(1), int64(0), float64(0), float64(1)}

	for _, raw := range shapes {
		v := viper.New()
		// GetConfig refuses a viper whose global-labels is absent, so this is the minimum a
		// caller has to seed rather than part of the case under test.
		v.Set("telemetry.global-labels", []any{})
		if raw != nil {
			v.Set(server.FlagInterBlockCache, raw)
		}

		replaced := cast.ToBool(v.Get(server.FlagInterBlockCache))
		cfg, err := serverconfig.GetConfig(v)
		if err != nil {
			t.Fatalf("inter-block-cache=%#v: GetConfig: %v", raw, err)
		}

		if cfg.InterBlockCache != replaced {
			t.Errorf("inter-block-cache=%#v (%T) resolves to %v through the struct field and %v "+
				"through the cast newApp runs today. One value an operator sets decides whether a "+
				"node builds a store cache manager, so moving that read while these disagree would "+
				"change it for this input",
				raw, raw, cfg.InterBlockCache, replaced)
		}
	}
}

// TestAdaptLegacyWithoutAppReportsAnUnresolvedApp pins the state that blocks read migration.
//
// The rollback command builds an app with no app.toml surface and no Tendermint config, and its
// tests supply no viper either. A read moved onto an App field would resolve to that field's zero
// value there rather than to what the operator set, so the state is named instead of implied.
func TestAdaptLegacyWithoutAppReportsAnUnresolvedApp(t *testing.T) {
	flat := newFlatView()

	got := seicfg.AdaptLegacyWithoutApp(flat)

	if got.Config.AppResolved() {
		t.Error("AdaptLegacyWithoutApp reports a resolved app.toml surface. It has none, and a read " +
			"moved onto an App field would then take a zero value for an operator's setting " +
			"wherever this producer is used")
	}
	if got.Flat != seicfg.FlatView(flat) {
		t.Error("AdaptLegacyWithoutApp dropped the flat view, which is the only channel its caller has")
	}
	if got.Config.Tendermint != nil {
		t.Error("AdaptLegacyWithoutApp invented a Tendermint config; the rollback command passes none")
	}
}

// TestAdaptLegacyReportsAResolvedApp is the other half, so AppResolved is not simply always false.
func TestAdaptLegacyReportsAResolvedApp(t *testing.T) {
	got := seicfg.AdaptLegacy(*serverconfig.DefaultConfig(), config.DefaultConfig(), newFlatView())

	if !got.Config.AppResolved() {
		t.Error("AdaptLegacy reports an unresolved app.toml surface even though it was handed one, " +
			"so the flag says nothing and a reader cannot use it to tell the two producers apart")
	}
}
