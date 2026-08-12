//go:build configv2

// This file and spec_test.go are the acceptance specification for the second
// configuration implementation. They are declared before they are built, so the gates in
// spec_test.go fail against these stubs and go green only when the implementation is real.
//
// Both are behind the configv2 build tag so CI stays green while they are red. Removing the
// tag is the definition of proof-of-concept complete: it means every gate passes, and it is
// mechanical rather than a judgement call about whether the work is finished.
//
// Three decisions are not made here, and an implementer must not make them by picking
// whichever reading is convenient. Each is marked NEEDS DECISION at the declaration it
// changes.

package seicfg

import (
	"errors"

	"github.com/spf13/cobra"
)

// ErrNotImplemented is returned by every part of this surface that the acceptance spec
// declares and nothing implements yet. It exists so a gate fails with a legible message
// rather than a nil dereference.
var ErrNotImplemented = errors.New("seicfg: declared by the acceptance spec, not implemented")

// Layer is one configuration source's contribution, before precedence is applied.
//
// Values holds the source's own keys and nothing else. A layer that consulted a second
// source would make precedence unobservable, because there would be no way to tell which
// source an answer came from, which is the property gate 1 exists to hold.
type Layer struct {
	// Source names which provider produced this layer, and must appear in Precedence.
	Source string
	// Values are the keys this source declares, spelled as the source spells them.
	Values map[string]any
}

// Provider reads one configuration source into one Layer.
type Provider interface {
	// Source names this provider's layer. It is the value that appears in Precedence.
	Source() string
	// Load reads the source. An absent source is not an error and yields an empty layer,
	// since a node with no app.toml is an ordinary node. An unreadable one is an error.
	Load() (Layer, error)
}

// Precedence is the declared order in which layers win, lowest to highest.
//
// The legacy path has no equivalent. Its order is an emergent property of which viper
// instance a caller asked, which is why two different orders are observable across the key
// set and why 17 keys have IsSet and Get disagreeing. Stating it as data is what lets a
// diagnostic tell a node operator that their environment variable beat their file.
var Precedence = []string{"default", "file", "env", "flag"}

// DefaultProvider yields the in-code defaults.
func DefaultProvider() Provider { return unbuilt{"default"} }

// FileProvider yields one TOML file's keys. An absent file yields an empty layer.
func FileProvider(path string) Provider { return unbuilt{"file"} }

// EnvProvider yields the environment's keys under a prefix.
//
// The prefix is a parameter rather than being derived from the running binary's filename,
// which is what the legacy path does through path.Base(os.Executable()). Deriving it means
// renaming the binary moves the entire namespace.
func EnvProvider(prefix string) Provider { return unbuilt{"env"} }

// FlagProvider yields a command's flag values.
//
// It reports a flag's value and never consults pflag's Changed bit. The legacy path forges
// that bit, because bindFlags calls Set for any key viper already holds, so a value from
// app.toml makes cobra report the flag as explicitly passed. One reader panics on it.
func FlagProvider(cmd *cobra.Command) Provider { return unbuilt{"flag"} }

// Resolve reduces layers to a Config, applying precedence and defaults once.
//
// NEEDS DECISION: absent versus zero. Config's fields are plain, so a key an operator
// left unset is indistinguishable from one they set to the field's zero value. That is
// wrong for at least 94 keys whose declared defaults disagree with each other, and for
// minimum-gas-prices, whose flag default is empty while its declared default is not. The
// decision changes this signature. Threading an optional type through 153 read sites taxes
// every reader to serve a validator that is not a reader; carrying a provenance side table
// beside Config does not. Do not pick one by implementing it.
func Resolve(layers []Layer) (Config, error) {
	return Config{}, ErrNotImplemented
}

// View projects a FlatView off a Config, for reads that still index configuration by string.
//
// The direction is load-bearing. A Config produces a view, never the reverse. Reversed,
// Config inherits the untyped map's ambiguity and is a wrapper rather than a seam.
//
// NEEDS DECISION: how the view is backed. Deriving it from the schema is the clean
// direction and cannot serve a key with no field, and 109 app.toml keys have none reachable
// from the structs the characterization harness reflects, so a derived view needs schema
// completeness across all 481 keys. Backing it with a viper built only for the migration
// window keeps the precondition at the differential instead. The signature is the same
// either way, so this decision is invisible here and very visible in gate 4.
func View(c Config) FlatView {
	return unbuiltView{}
}

// unbuilt is a Provider that reports its source and refuses to load.
type unbuilt struct{ source string }

func (u unbuilt) Source() string       { return u.source }
func (u unbuilt) Load() (Layer, error) { return Layer{Source: u.source}, ErrNotImplemented }

// unbuiltView answers nothing, so gate 4 fails on the first key rather than silently
// resolving every key to nil.
type unbuiltView struct{}

func (unbuiltView) Get(string) any { return nil }
