// Package declared turns a JSON declaration of a supplier relay into an adapter, so adding an in-house relay is a
// deployment file rather than a Go package. It compiles every operation's paths and rules at startup, keeps the
// declaration's schema versioned, and implements the same adapter contracts as the built-in adapters.
package declared
