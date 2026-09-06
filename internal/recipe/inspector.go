package recipe

import _ "embed"

// Inspector is the Python script run inside the build sandbox to produce a
// Manifest; it is written next to the clone and invoked with the repo path.
//
//go:embed inspect.py
var Inspector string

// InspectorFile is the name the script is written under in the sandbox.
const InspectorFile = "inspect.py"
