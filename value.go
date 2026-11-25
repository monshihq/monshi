package monshi

import (
	"time"

	"github.com/monshihq/monshi/internal/engine"
	"github.com/monshihq/monshi/internal/program"
	"github.com/shopspring/decimal"
)

// Value is a typed value supplied to an edit or returned by a case read.
type Value = engine.Value

// Text constructs a string value.
func Text(s string) Value { return engine.Text(s) }

// Int constructs an integer value.
func Int(i int64) Value { return engine.Int(i) }

// Decimal constructs an exact decimal value.
func Decimal(d decimal.Decimal) Value { return engine.Decimal(d) }

// Bool constructs a boolean value.
func Bool(b bool) Value { return engine.Bool(b) }

// Date constructs a date value.
func Date(t time.Time) Value { return engine.Date(t) }

// DateTime constructs a datetime value.
func DateTime(t time.Time) Value { return engine.DateTime(t) }

// Tag constructs an enum value by tag name.
func Tag(name string) Value { return engine.Tag(name) }

// Binary constructs a value referring to retained binary content.
func Binary(ref BinaryRef) Value { return engine.Binary(ref) }

// User constructs a role-field value naming a user.
func User(id UserID) Value { return engine.User(id) }

// ValueKind identifies a value's payload class.
type ValueKind = engine.ValueKind

const (
	KindNone     = engine.KindNone
	KindText     = engine.KindText
	KindInteger  = engine.KindInteger
	KindDecimal  = engine.KindDecimal
	KindBoolean  = engine.KindBoolean
	KindDate     = engine.KindDate
	KindDatetime = engine.KindDatetime
	KindBinary   = engine.KindBinary
	KindTag      = engine.KindTag
	KindUser     = engine.KindUser
)

// Edit is one operation in an atomic Update batch.
type Edit = engine.Edit

// Set assigns a typed value at path.
func Set(path string, v Value) Edit { return engine.Set(path, v) }

// Empty clears a field without removing its declaration.
func Empty(path string) Edit { return engine.Empty(path) }

// Add appends an instance to a collection.
func Add(path string) Edit { return engine.Add(path) }

// Remove removes the instance addressed by path.
func Remove(path string) Edit { return engine.Remove(path) }

// BinaryRef is an opaque reference to binary content retained by the host.
type BinaryRef = program.BinaryRef
