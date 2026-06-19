// Package soap is payjet's internal SOAP 1.1 transport, shared by the gateways
// that talk to Shaparak's XML web services (Mellat, Parsian). It owns only the
// transport concerns common to every SOAP call — POSTing an envelope, setting
// the SOAPAction header, reading the body, and turning HTTP and soap:Fault
// failures into structured *errorx.Error values. Each gateway still builds its own
// envelope and parses its own response, because those differ per bank.
//
// It lives in internal/ on purpose: SOAP is niche, so it is a payjet
// implementation detail rather than part of the public SDK surface.
package soap

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hatami57/microjet/core/errorx"
)

// Post sends envelope to url as a SOAP 1.1 request using httpClient. action is
// written to the SOAPAction header verbatim (callers pass exactly what their
// bank expects — a bare operation name, a namespaced URL, quoted or not). On a
// non-2xx response or a soap:Fault it returns a *errorx.Error (Internal); on
// success it returns the raw response body for the caller to parse.
func Post(ctx context.Context, httpClient *http.Client, url, action, envelope string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(envelope))
	if err != nil {
		return nil, errorx.NewInternalError("soap", "building request failed").WithInner(err)
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", action)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, errorx.NewInternalError("soap", "request failed").
			WithParams("url", url).WithInner(err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errorx.NewInternalError("soap", fmt.Sprintf("upstream returned %d", resp.StatusCode)).
			WithParams("status", resp.StatusCode, "body", string(data))
	}
	if code, str, ok := fault(data); ok {
		return nil, errorx.NewInternalError("soap", "SOAP fault").
			WithParams("faultcode", code, "faultstring", str, "action", action)
	}
	return data, nil
}

// fault scans a SOAP response for a soap:Fault element (any namespace prefix)
// and returns its faultcode/faultstring. It streams tokens by local name, so it
// works regardless of the prefix the server uses (soap:, soapenv:, S:).
func fault(body []byte) (faultcode, faultstring string, found bool) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var capture string // local name of the element whose text we want next
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Fault":
				found = true
			case "faultcode", "Code", "faultstring", "Reason", "Text":
				capture = t.Name.Local
			}
		case xml.CharData:
			if capture == "" {
				continue
			}
			text := strings.TrimSpace(string(t))
			switch capture {
			case "faultcode", "Code":
				if faultcode == "" {
					faultcode = text
				}
			case "faultstring", "Reason", "Text":
				if faultstring == "" {
					faultstring = text
				}
			}
			capture = ""
		}
	}
	return faultcode, faultstring, found
}
