package agentruntime

import "dayorder.local/api/internal/canonicaljson"

func canonicalJSON(value any) ([]byte, error) {
	return canonicaljson.Value(value)
}

func canonicalJSONBytes(raw []byte) ([]byte, error) {
	return canonicaljson.Bytes(raw)
}
