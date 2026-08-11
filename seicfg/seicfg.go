// Package seicfg holds a seid node's configuration as one materialized value.
//
// A node's configuration reaches running code through two channels: a Tendermint config
// struct and a flat key-value map that callers index by string and cast at the read site.
// Config carries both, so a caller takes one value rather than two, and so a second
// implementation can be held against the legacy one by comparing values of a single type.
//
// Four roles divide the work of getting a value from a file to a reader. Naming them here
// reserves the vocabulary, because only the last one exists today.
//
//	Provider  a configuration source, read into one layer of raw values. One per source:
//	          in-code defaults, a TOML file, the environment, cobra flags.
//	Resolve   the layers reduced to a Config, applying precedence and defaults once. This
//	          is the step the legacy path never named, which is why precedence there is an
//	          emergent property of which viper instance a caller asked rather than a
//	          declared order.
//	View      a FlatView projected off a Config, for reads that still index by string.
//	          The direction is load-bearing: a Config produces a view, never the reverse.
//	          Reversed, Config inherits the untyped map's ambiguity and is a wrapper
//	          rather than a seam.
//	Adapt     the legacy path treated as one opaque Provider and Resolve, its output
//	          wrapped so it satisfies this package's type. AdaptLegacy below.
//
// Provider, Resolve and View are not implemented. A second implementation needs View
// first, because it has no viper to hand through and every unmigrated read goes through
// the flat view. Whether View derives from the schema, which cannot serve a key with no
// field, or is backed by a viper the implementation builds for the migration window, is
// open.
package seicfg

import (
	serverconfig "github.com/sei-protocol/sei-chain/sei-cosmos/server/config"
	tmcfg "github.com/sei-protocol/sei-chain/sei-tendermint/config"
)

// FlatView is the key-value view of a configuration, indexed by string and cast by its caller.
//
// It is declared here rather than taken from sei-cosmos/server/types so that package can name
// a Resolved without an import cycle. The method set is identical to servertypes.AppOptions, so
// a *viper.Viper satisfies both and a FlatView is assignable to an AppOptions.
type FlatView interface {
	Get(string) any
}

// Config is a seid node's resolved configuration.
//
// App and Tendermint are named fields rather than embedded ones. Both underlying types are
// called Config and both embed a BaseConfig, so embedding the pair does not compile; promoting
// their fields onto this type has to be done by hand, one key at a time, which is also where a
// key that both files declare gets resolved rather than shadowed by promotion depth.
type Config struct {
	// App is the app.toml surface.
	App serverconfig.Config

	// Tendermint is the config.toml surface. It is nil where the caller has none, which today
	// is the rollback command.
	Tendermint *tmcfg.Config

	// appResolved is false where App is the zero value rather than a resolved configuration.
	// It exists so that state is a named one: rollback builds an app without an app.toml
	// surface, and a read that moved onto an App field would otherwise silently see false or
	// zero there instead of what the operator set.
	appResolved bool
}

// AppResolved reports whether App holds a resolved configuration.
//
// A read that has moved off the flat view and onto an App field must not be reachable from a
// caller where this is false. Today the only such caller is the rollback command.
func (c Config) AppResolved() bool {
	return c.appResolved
}

// Resolved is a Config together with the flat view serving reads not yet moved onto a field.
//
// The two are separate types because Config is the unit two resolvers get compared as. A flat
// view inside it would make that comparison depend on both producers carrying a structurally
// identical one, and a resolver that has stopped building a viper cannot. Keeping it beside
// Config means reflect.DeepEqual over the Config alone is the whole differential.
type Resolved struct {
	Config Config

	// Flat serves every read that still indexes configuration by string. Each read that moves
	// onto a Config field retires one of its callers, and the field goes when the last one does.
	Flat FlatView
}

// AdaptLegacy wraps what the legacy interception handler resolved.
//
// It only gathers what its caller already holds, so it cannot resolve differently from the
// handler that produced its arguments. That is what makes introducing this type a change in
// shape rather than in behaviour. It resolves nothing itself, which is why it is an adapter
// over the legacy path rather than one generation of a resolver.
func AdaptLegacy(app serverconfig.Config, tendermint *tmcfg.Config, flat FlatView) Resolved {
	return Resolved{
		Config: Config{App: app, Tendermint: tendermint, appResolved: true},
		Flat:   flat,
	}
}

// AdaptLegacyWithoutApp wraps a legacy caller that holds no app.toml surface.
//
// The rollback command is the only one, and it holds no Tendermint config either. Its tests
// run with a nil viper as well, so resolving the app.toml surface here would panic rather
// than degrade. Config.App is the zero value and Config.AppResolved reports false.
func AdaptLegacyWithoutApp(flat FlatView) Resolved {
	return Resolved{Flat: flat}
}
