package validator

import "errors"

// ErrSchemaCompile is returned when schema bytes cannot be compiled into a
// usable JSON Schema. Callers may unwrap for the underlying jsonschema error.
var ErrSchemaCompile = errors.New("validator: schema compile error")
