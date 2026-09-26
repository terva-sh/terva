// Package permission is terva's permission model: the approval modes, the
// typed rules and the policy that evaluates them, the Authority classes, the
// confirm gate and its Confirmer callback, and the approval classifier.
//
// The engine enforces none of it. packages/core calls only the agent's
// core.Gate, and keeps Gate, GateFunc, AllowAll and ReadOnlySet. A ConfirmGate
// is one Gate a host may choose, the way it may choose the stall or lazytools
// components.
//
// The package imports only packages/core and packages/core/i18n from terva,
// so any caller of core can import it without a cycle.
package permission
