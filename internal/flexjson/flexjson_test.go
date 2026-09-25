package flexjson

import (
	"encoding/json"
	"testing"
)

func TestStringDecodesStringsAndNumbers(t *testing.T) {
	var v struct{ A, B, C String }
	if err := json.Unmarshal([]byte(`{"A":"100","B":10012,"C":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != "100" || v.B != "10012" || v.C != "" {
		t.Errorf("got %+v", v)
	}
}

func TestStringRejectsObjects(t *testing.T) {
	var v String
	if err := json.Unmarshal([]byte(`{"x":1}`), &v); err == nil {
		t.Error("expected an error for an object")
	}
}
