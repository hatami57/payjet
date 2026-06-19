package soap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
)

func TestPostSendsEnvelopeAndActionVerbatim(t *testing.T) {
	var gotBody, gotAction, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAction = r.Header.Get("SOAPAction")
		gotContentType = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`<Envelope><Body><Return>0,123</Return></Body></Envelope>`))
	}))
	defer srv.Close()

	out, err := Post(context.Background(), srv.Client(), srv.URL,
		"http://interfaces.core.sw.bps.com/bpPayRequest", `<int:bpPayRequest/>`)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotBody != `<int:bpPayRequest/>` {
		t.Errorf("body = %q, want verbatim envelope", gotBody)
	}
	// Action is sent exactly as given — no auto-quoting.
	if gotAction != "http://interfaces.core.sw.bps.com/bpPayRequest" {
		t.Errorf("SOAPAction = %q, want verbatim", gotAction)
	}
	if !strings.HasPrefix(gotContentType, "text/xml") {
		t.Errorf("Content-Type = %q, want text/xml", gotContentType)
	}
	if !strings.Contains(string(out), "<Return>0,123</Return>") {
		t.Errorf("response = %s", out)
	}
}

func TestPostDetectsFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<soapenv:Body><soapenv:Fault>` +
			`<faultcode>soapenv:Server</faultcode><faultstring>bad request</faultstring>` +
			`</soapenv:Fault></soapenv:Body></soapenv:Envelope>`))
	}))
	defer srv.Close()

	_, err := Post(context.Background(), srv.Client(), srv.URL, "X", `<x/>`)
	if err == nil {
		t.Fatal("expected error for SOAP fault")
	}
	if !errorx.IsInternalError(err) {
		t.Errorf("error type = %v, want Internal", err)
	}
	if ce := errorx.GetError(err); ce == nil || ce.Params["faultstring"] != "bad request" {
		t.Errorf("params = %+v, want faultstring 'bad request'", ce)
	}
}

func TestPostHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Post(context.Background(), srv.Client(), srv.URL, "X", `<x/>`); err == nil {
		t.Fatal("expected error for 500 response")
	}
}
