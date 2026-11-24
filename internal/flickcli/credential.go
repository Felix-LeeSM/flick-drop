package flickcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The FLCR1 payload from contracts/credential-payload.schema.json, twin of
// web/src/lib/credentials/serialize.ts. It rides the text-secret path.
const (
	credentialPrefix = "FLCR1:"
	credentialMask   = "••••••••"
)

type CredentialField struct {
	ID     string
	Label  string
	Value  string
	Secret bool
}

type Credential struct {
	Type   string // login, card, identity, custom
	Title  string
	Notes  string
	Fields []CredentialField
}

// Detection on open: any malformed payload falls back to plain text, like the browser.
func ParseCredential(plaintext []byte) (Credential, bool) {
	if !bytes.HasPrefix(plaintext, []byte(credentialPrefix)) {
		return Credential{}, false
	}
	cred, err := parseCredentialEnvelope(bytes.TrimPrefix(plaintext, []byte(credentialPrefix)))
	if err != nil {
		return Credential{}, false
	}
	return cred, true
}

// ParseCredentialInput reads sender-supplied JSON, with or without the FLCR1 prefix,
// and reports why it is rejected so a bad payload fails before encryption.
func ParseCredentialInput(data []byte) (Credential, error) {
	return parseCredentialEnvelope(bytes.TrimPrefix(bytes.TrimSpace(data), []byte(credentialPrefix)))
}

// SerializeCredential emits the same compact bytes the web create page produces:
// key order v, type, fields, title, notes and field order id, label, value, secret.
func SerializeCredential(cred Credential) string {
	fields := make([]credentialFieldJSON, len(cred.Fields))
	for i, f := range cred.Fields {
		fields[i] = credentialFieldJSON{ID: f.ID, Label: f.Label, Value: f.Value, Secret: f.Secret}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(credentialEnvelopeJSON{
		V:      1,
		Type:   cred.Type,
		Fields: fields,
		Title:  cred.Title,
		Notes:  cred.Notes,
	}); err != nil {
		return credentialPrefix
	}
	return credentialPrefix + string(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

type credentialEnvelopeJSON struct {
	V      int                   `json:"v"`
	Type   string                `json:"type"`
	Fields []credentialFieldJSON `json:"fields"`
	Title  string                `json:"title"`
	Notes  string                `json:"notes"`
}

type credentialFieldJSON struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// Validation mirrors parseCredential in web/src/lib/credentials/serialize.ts:
// only the five envelope keys, v === 1, a known type, and exact field shapes.
func parseCredentialEnvelope(raw []byte) (Credential, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Credential{}, fmt.Errorf("credential is not a JSON object: %w", err)
	}
	for key := range root {
		switch key {
		case "v", "type", "fields", "title", "notes":
		default:
			return Credential{}, fmt.Errorf("credential has an unknown key %q", key)
		}
	}

	v, err := credentialNumber(root["v"])
	if err != nil {
		return Credential{}, fmt.Errorf(`credential "v" must be 1`)
	}
	if v != 1 {
		return Credential{}, fmt.Errorf(`credential "v" must be 1, got %v`, v)
	}

	typeRaw, ok := root["type"]
	if !ok {
		return Credential{}, errors.New(`credential is missing "type"`)
	}
	kind, ok := credentialString(typeRaw)
	if !ok {
		return Credential{}, errors.New(`credential "type" must be a string`)
	}
	switch kind {
	case "login", "card", "identity", "custom":
	default:
		return Credential{}, fmt.Errorf(`credential "type" %q is not one of login, card, identity, custom`, kind)
	}

	fieldsRaw, ok := root["fields"]
	if !ok {
		return Credential{}, errors.New(`credential is missing "fields"`)
	}
	var rawFields []json.RawMessage
	if err := json.Unmarshal(fieldsRaw, &rawFields); err != nil || rawFields == nil {
		return Credential{}, errors.New(`credential "fields" must be an array`)
	}

	cred := Credential{Type: kind}
	if raw, ok := root["title"]; ok {
		title, ok := credentialString(raw)
		if !ok {
			return Credential{}, errors.New(`credential "title" must be a string`)
		}
		cred.Title = title
	}
	if raw, ok := root["notes"]; ok {
		notes, ok := credentialString(raw)
		if !ok {
			return Credential{}, errors.New(`credential "notes" must be a string`)
		}
		cred.Notes = notes
	}

	cred.Fields = make([]CredentialField, 0, len(rawFields))
	for i, rawField := range rawFields {
		field, err := parseCredentialField(rawField)
		if err != nil {
			return Credential{}, fmt.Errorf("credential field %d: %w", i, err)
		}
		cred.Fields = append(cred.Fields, field)
	}
	return cred, nil
}

func parseCredentialField(raw json.RawMessage) (CredentialField, error) {
	var field map[string]json.RawMessage
	if err := json.Unmarshal(raw, &field); err != nil {
		return CredentialField{}, errors.New("is not a JSON object")
	}
	for key := range field {
		switch key {
		case "id", "label", "value", "secret":
		default:
			return CredentialField{}, fmt.Errorf("has an unknown key %q", key)
		}
	}
	var out CredentialField
	id, ok := credentialString(field["id"])
	if !ok {
		return CredentialField{}, errors.New(`is missing a string "id"`)
	}
	out.ID = id
	label, ok := credentialString(field["label"])
	if !ok {
		return CredentialField{}, errors.New(`is missing a string "label"`)
	}
	out.Label = label
	value, ok := credentialString(field["value"])
	if !ok {
		return CredentialField{}, errors.New(`is missing a string "value"`)
	}
	out.Value = value
	secret, ok := credentialBool(field["secret"])
	if !ok {
		return CredentialField{}, errors.New(`is missing a boolean "secret"`)
	}
	out.Secret = secret
	return out, nil
}

// JSON null unmarshals into Go scalars without error, so types are checked through
// interface{} the way the browser's typeof checks do.
func credentialNumber(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	n, ok := v.(float64)
	if !ok {
		return 0, errors.New("not a number")
	}
	return n, nil
}

func credentialString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func credentialBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// RevealText is the -reveal rendering: the values the recipient asked to see,
// in the label-colon-value shape of CredentialView's copy-all output.
func (c Credential) RevealText() string {
	var lines []string
	if c.Title != "" {
		lines = append(lines, c.Title)
	}
	if c.Notes != "" {
		lines = append(lines, c.Notes)
	}
	for _, f := range c.Fields {
		lines = append(lines, fmt.Sprintf("%s: %s", fieldLabel(f), f.Value))
	}
	return strings.Join(lines, "\n")
}

// SummaryLines is the masked stderr preview: secret field values never appear,
// matching CredentialView's masked display.
func (c Credential) SummaryLines() []string {
	header := c.Type
	if strings.TrimSpace(c.Title) != "" {
		header += " — " + strings.TrimSpace(c.Title)
	}
	lines := []string{"Credential " + header}
	for _, f := range c.Fields {
		value := f.Value
		if f.Secret {
			value = credentialMask
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", fieldLabel(f), value))
	}
	if strings.TrimSpace(c.Notes) != "" {
		lines = append(lines, "  Notes: "+c.Notes)
	}
	return lines
}

// JSONMap is the -json shape. Secret field values appear only when the caller
// passed -reveal; otherwise they exist only in the saved file.
func (c Credential) JSONMap(reveal bool) map[string]any {
	fields := make([]map[string]any, 0, len(c.Fields))
	for _, f := range c.Fields {
		field := map[string]any{"label": fieldLabel(f), "secret": f.Secret}
		if reveal || !f.Secret {
			field["value"] = f.Value
		}
		fields = append(fields, field)
	}
	return map[string]any{
		"type":   c.Type,
		"title":  c.Title,
		"notes":  c.Notes,
		"fields": fields,
	}
}

// An empty label would render as ": value"; the browser keeps empty labels too,
// so only the display falls back.
func fieldLabel(f CredentialField) string {
	if f.Label == "" {
		return f.ID
	}
	return f.Label
}
