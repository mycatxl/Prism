package state

import "errors"

// ErrNotFound is returned when a requested resource does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a write violates a uniqueness/conflict constraint.
var ErrConflict = errors.New("conflict")

// ErrInsufficientBalance is returned when a movement would take a balance below
// zero. There is no overdraft: a charge larger than the balance is refused
// outright rather than applied and left negative.
var ErrInsufficientBalance = errors.New("insufficient balance")

// ErrBalanceOverflow is returned when a credit would exceed the signed balance
// representation. Refusing the movement preserves the non-negative balance
// invariant; wrapping to a negative value would turn a credit into an
// accidental overdraft.
var ErrBalanceOverflow = errors.New("balance overflow")
