package session

import (
	"bytes"
	"encoding/json"
)

func jsonDecoder(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec
}
