package validator

import "errors"

// ErrSchemaCompile is returned when schema bytes cannot be compiled into a
// usable JSON Schema. Callers may unwrap for the underlying jsonschema error.
var ErrSchemaCompile = errors.New("validator: schema compile error")

// ErrStrictRewrite is returned by [WrapStrict] when schema bytes cannot be
// parsed or re-marshaled during the strict-mode injection pass.
var ErrStrictRewrite = errors.New("validator: strict rewrite error")
