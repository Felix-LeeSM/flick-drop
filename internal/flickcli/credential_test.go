package flickcli

import (
	"strings"
	"testing"
	"time"
)

// The exact bytes web/src/lib/credentials/serialize.ts produces for a web-created login.
const webCredential = `FLCR1:{"v":1,"type":"login","fields":[{"id":"username","label":"Username","value":"alice","secret":false},{"id":"password","label":"Password","value":"hunter2","secret":true}],"title":"","notes":""}`

func TestParseCredentialAcceptsTheWebBytes(t *testing.T) {
	withTimeout(t, time.Minute)
	cred, ok := ParseCredential([]byte(webCredential))
	if !ok {
		t.Fatal("the web serialization was not recognized")
	}
	if cred.Type != "login" || cred.Title != "" || cred.Notes != "" {
		t.Errorf("envelope = %+v", cred)
	}
	if len(cred.Fields) != 2 {
		t.Fatalf("fields = %+v", cred.Fields)
	}
	if cred.Fields[0] != (CredentialField{ID: "username", Label: "Username", Value: "alice", Secret: false}) {
		t.Errorf("field 0 = %+v", cred.Fields[0])
	}
	if cred.Fields[1] != (CredentialField{ID: "password", Label: "Password", Value: "hunter2", Secret: true}) {
		t.Errorf("field 1 = %+v", cred.Fields[1])
	}
}

func TestSerializeCredentialReproducesTheWebBytes(t *testing.T) {
	withTimeout(t, time.Minute)
	cred, ok := ParseCredential([]byte(webCredential))
	if !ok {
		t.Fatal("parse failed")
	}
	if re := SerializeCredential(cred); re != webCredential {
		t.Errorf("serialize =\n%s\nwant\n%s", re, webCredential)
	}
}

func TestSerializeCredentialDoesNotHTMLEscape(t *testing.T) {
	withTimeout(t, time.Minute)
	cred := Credential{Type: "custom", Fields: []CredentialField{
		{ID: "f", Label: "L", Value: "a<b&c", Secret: false},
	}}
	re := SerializeCredential(cred)
	if strings.Contains(re, `\u003c`) {
		t.Errorf("serialize escaped HTML: %s", re)
	}
	if !strings.Contains(re, `"value":"a<b&c"`) {
		t.Errorf("serialize = %s", re)
	}
}

func TestParseCredentialRejectsMalformedPayloads(t *testing.T) {
	withTimeout(t, time.Minute)
	for name, value := range map[string]string{
		"no prefix":         `{"v":1,"type":"login","fields":[]}`,
		"not json":          "FLCR1:not json",
		"not an object":     `FLCR1:[]`,
		"wrong version":     `FLCR1:{"v":2,"type":"login","fields":[]}`,
		"string version":    `FLCR1:{"v":"1","type":"login","fields":[]}`,
		"unknown type":      `FLCR1:{"v":1,"type":"wallet","fields":[]}`,
		"missing type":      `FLCR1:{"v":1,"fields":[]}`,
		"missing fields":    `FLCR1:{"v":1,"type":"login"}`,
		"null fields":       `FLCR1:{"v":1,"type":"login","fields":null}`,
		"unknown key":       `FLCR1:{"v":1,"type":"login","fields":[],"extra":1}`,
		"null title":        `FLCR1:{"v":1,"type":"login","fields":[],"title":null}`,
		"numeric title":     `FLCR1:{"v":1,"type":"login","fields":[],"title":5}`,
		"field not object":  `FLCR1:{"v":1,"type":"login","fields":["x"]}`,
		"field extra key":   `FLCR1:{"v":1,"type":"login","fields":[{"id":"a","label":"L","value":"v","secret":false,"x":1}]}`,
		"field no secret":   `FLCR1:{"v":1,"type":"login","fields":[{"id":"a","label":"L","value":"v"}]}`,
		"field null secret": `FLCR1:{"v":1,"type":"login","fields":[{"id":"a","label":"L","value":"v","secret":null}]}`,
		"field null value":  `FLCR1:{"v":1,"type":"login","fields":[{"id":"a","label":"L","value":null,"secret":false}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseCredential([]byte(value)); ok {
				t.Errorf("accepted %q", value)
			}
			// The input path shares every check except that its FLCR1 prefix is optional.
			if name != "no prefix" {
				if _, err := ParseCredentialInput([]byte(value)); err == nil {
					t.Errorf("input-accepted %q", value)
				}
			}
		})
	}
}

func TestParseCredentialInputAcceptsJSONWithoutThePrefix(t *testing.T) {
	withTimeout(t, time.Minute)
	cred, err := ParseCredentialInput([]byte(`{"v":1,"type":"card","title":"personal","fields":[{"id":"number","label":"Number","value":"4111","secret":true}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cred.Type != "card" || len(cred.Fields) != 1 || !cred.Fields[0].Secret {
		t.Errorf("credential = %+v", cred)
	}
	// Omitted notes serialize as the empty string the web always emits.
	if got := SerializeCredential(cred); !strings.Contains(got, `"title":"personal","notes":""`) {
		t.Errorf("serialize = %s", got)
	}

	if _, err := ParseCredentialInput([]byte(`{"type":"card","fields":[]}`)); err == nil {
		t.Error("input without \"v\" was accepted")
	}
}

func TestCredentialRenderings(t *testing.T) {
	withTimeout(t, time.Minute)
	cred, ok := ParseCredential([]byte(webCredential))
	if !ok {
		t.Fatal("parse failed")
	}

	if got, want := cred.RevealText(), "Username: alice\nPassword: hunter2"; got != want {
		t.Errorf("RevealText = %q, want %q", got, want)
	}

	summary := strings.Join(cred.SummaryLines(), "\n")
	if strings.Contains(summary, "hunter2") {
		t.Errorf("summary leaked the secret value: %q", summary)
	}
	if !strings.Contains(summary, "Password: ••••••••") || !strings.Contains(summary, "Username: alice") {
		t.Errorf("summary = %q", summary)
	}

	masked := cred.JSONMap(false)
	if fields := masked["fields"].([]map[string]any); len(fields) == 2 {
		if _, has := fields[1]["value"]; has {
			t.Errorf("masked JSONMap kept the secret value: %v", masked)
		}
		if fields[0]["value"] != "alice" {
			t.Errorf("masked JSONMap hid a non-secret value: %v", masked)
		}
	} else {
		t.Errorf("masked JSONMap fields = %v", masked)
	}
}
