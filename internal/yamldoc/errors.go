package yamldoc

import "errors"

// ErrMalformedYAML is returned when the YAML source cannot be parsed.
var ErrMalformedYAML = errors.New("yamldoc: malformed YAML")

// ErrPointerNotFound is returned when a JSON Pointer lookup finds no matching
// entry in a PositionMap.
var ErrPointerNotFound = errors.New("yamldoc: pointer not found")
