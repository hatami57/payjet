// Package flexjson holds JSON types for bank APIs that are loose about types.
package flexjson

import "encoding/json"

// String decodes a JSON string or number into its text. IDPay documents status,
// track_id and amount as strings but has also sent them as numbers; Zarinpal's
// ref_id arrives either way too.
type String string

func (f *String) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = String(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = String(n.String())
	return nil
}
