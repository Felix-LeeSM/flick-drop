package flickcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Cassette record/replay: the tests below run the CLI flows against responses
// recorded from a real deployment, so a change in the CLI's request sequence
// or in how it reads the API's answers fails in CI without network access.
// Replay only shows the API's past answers: a live regression needs the
// canary (scripts/smoke/canary.sh) or scripts/smoke/live.sh.
//
// Record once by hand against a deployment:
//
//	FLICK_CASSETTE_RECORD=https://flick.example go test ./internal/flickcli/ -run TestCassette -count=1 -timeout 600s
//
// Recording creates real secrets on that deployment and consumes them in the
// same run; every payload is a fake marker value, never a real secret. CI runs
// without FLICK_CASSETTE_RECORD and replays the committed fixtures from
// testdata/cassettes/.

const cassetteRecordEnv = "FLICK_CASSETTE_RECORD"

type cassetteInteraction struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

type cassetteFile struct {
	Comment      string                `json:"comment"`
	Identities   map[string]string     `json:"identities,omitempty"`
	Interactions []cassetteInteraction `json:"interactions"`
}

// responseHeaders we store: everything the CLI client reads back from a response.
var cassetteHeaderNames = []string{
	"Content-Type",
	"Flick-Kind",
	"Flick-Content-Type",
	"Flick-Expires-At",
	"Flick-Encrypted-Filename",
	"Flick-Wrapped-Identity",
}

type cassetteTransport struct {
	t       *testing.T
	mu      sync.Mutex
	record  string // deployment base URL while recording; "" replays
	fixture string
	file    cassetteFile
	idx     int
}

func (tr *cassetteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.record != "" {
		return tr.recordInteraction(req)
	}
	return tr.replayInteraction(req)
}

func (tr *cassetteTransport) recordInteraction(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("cassette recording against %s: %w", tr.record, err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

	path := req.URL.RequestURI()
	interaction := cassetteInteraction{Method: req.Method, Path: path, Status: resp.StatusCode, Body: body}
	interaction.Header = map[string][]string{}
	for _, name := range cassetteHeaderNames {
		if values := resp.Header.Values(name); len(values) > 0 {
			interaction.Header[name] = values
		}
	}
	tr.file.Interactions = append(tr.file.Interactions, interaction)
	tr.t.Logf("recorded %s %s -> %d (%d bytes)", req.Method, path, resp.StatusCode, len(body))
	return resp, nil
}

func (tr *cassetteTransport) replayInteraction(req *http.Request) (*http.Response, error) {
	if tr.idx >= len(tr.file.Interactions) {
		return nil, fmt.Errorf("cassette %s has no interaction left for %s %s", tr.fixture, req.Method, req.URL.RequestURI())
	}
	next := tr.file.Interactions[tr.idx]
	path := req.URL.RequestURI()
	if next.Method != req.Method || next.Path != path {
		return nil, fmt.Errorf(
			"cassette %s drift at interaction %d: the flow sent %s %s, the recording has %s %s; re-record with %s=<url>",
			tr.fixture, tr.idx, req.Method, path, next.Method, next.Path, cassetteRecordEnv)
	}
	tr.idx++
	header := http.Header{}
	for name, values := range next.Header {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", next.Status, http.StatusText(next.Status)),
		StatusCode:    next.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(next.Body)),
		ContentLength: int64(len(next.Body)),
		Request:       req,
	}, nil
}

// Identity returns a pinned identity: generated and stored in the cassette
// while recording, parsed back from it while replaying.
func (tr *cassetteTransport) Identity(name string) *age.HybridIdentity {
	tr.t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.record != "" {
		identity, err := clientcrypto.NewIdentity()
		if err != nil {
			tr.t.Fatalf("generate %s identity: %v", name, err)
		}
		tr.file.Identities[name] = identity.String()
		return identity
	}
	encoded, ok := tr.file.Identities[name]
	if !ok {
		tr.t.Fatalf("cassette %s has no %s identity", tr.fixture, name)
	}
	identity, err := clientcrypto.ParseIdentity(encoded)
	if err != nil {
		tr.t.Fatalf("cassette %s: parse %s identity: %v", tr.fixture, name, err)
	}
	return identity
}

// withCassette runs a flow against the recorded responses, or records them
// first. Recording waits 15 seconds before each cassette, under the API's
// limit of 5 creates per client network per minute.
func withCassette(t *testing.T, name string, run func(t *testing.T, client *Client, tr *cassetteTransport)) {
	t.Helper()
	fixture := filepath.Join("testdata", "cassettes", name+".json")
	tr := &cassetteTransport{t: t, fixture: fixture, file: cassetteFile{Identities: map[string]string{}}}

	if base := os.Getenv(cassetteRecordEnv); base != "" {
		tr.record = strings.TrimRight(base, "/")
		t.Logf("recording %s against %s", name, tr.record)
		time.Sleep(15 * time.Second) // under the 5-creates-per-minute bucket, not on it
		// Only a flow that returned normally may replace a fixture: t.Failed is
		// still false while a panic unwinds, and a partial recording must not
		// be committed.
		completed := false
		defer func() {
			if t.Failed() || !completed {
				t.Logf("recording did not complete cleanly; fixture %s not written", fixture)
				return
			}
			tr.file.Comment = fmt.Sprintf(
				"Recorded from a real deployment by %s with %s=<url>; payloads are fake markers, consumed secrets, dead tokens. Re-record to refresh.",
				t.Name(), cassetteRecordEnv)
			encoded, err := json.MarshalIndent(tr.file, "", "  ")
			if err != nil {
				t.Fatalf("encode cassette: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(fixture), 0o755); err != nil {
				t.Fatalf("create fixture dir: %v", err)
			}
			if err := os.WriteFile(fixture, encoded, 0o644); err != nil {
				t.Fatalf("write cassette: %v", err)
			}
			t.Logf("wrote %s with %d interactions", fixture, len(tr.file.Interactions))
		}()
		client := NewClient(tr.record, &http.Client{Transport: tr})
		run(t, client, tr)
		completed = true
		return
	}

	// The cassettes are committed with the tests, so a missing one is a deleted
	// file that would silently drop coverage, not an environment without it.
	encoded, err := os.ReadFile(fixture)
	if os.IsNotExist(err) {
		t.Fatalf("cassette %s is missing; restore it or record it with %s=<deployment url>", fixture, cassetteRecordEnv)
	}
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if err := json.Unmarshal(encoded, &tr.file); err != nil {
		t.Fatalf("decode cassette %s: %v", fixture, err)
	}
	if len(tr.file.Interactions) == 0 {
		t.Fatalf("cassette %s is empty", fixture)
	}
	client := NewClient("https://cassette.invalid", &http.Client{Transport: tr})
	run(t, client, tr)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.idx != len(tr.file.Interactions) {
		t.Fatalf("cassette %s has %d interactions but the flow used %d", tr.fixture, len(tr.file.Interactions), tr.idx)
	}
}

func TestCassetteSendOpenText(t *testing.T) {
	withTimeout(t, 2*time.Minute)
	withCassette(t, "send-open-text", func(t *testing.T, client *Client, tr *cassetteTransport) {
		ctx := context.Background()
		payload := "cassette smoke payload — not a real secret"

		sent, err := Send(ctx, client, SendOptions{
			Text:     payload,
			TTL:      MinTTL,
			Identity: tr.Identity("sender"),
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		link, err := ParseShareLink(sent.Link)
		if err != nil {
			t.Fatalf("parse share link: %v", err)
		}
		if link.Identity == nil {
			t.Fatal("the model B link carries no identity")
		}

		opened, err := Open(ctx, client, OpenOptions{Link: link, OutputDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(opened.Plaintext) != payload {
			t.Errorf("opened %q, want %q", opened.Plaintext, payload)
		}
	})
}

func TestCassetteSendOpenPassphrase(t *testing.T) {
	withTimeout(t, 2*time.Minute)
	withCassette(t, "send-open-passphrase", func(t *testing.T, client *Client, tr *cassetteTransport) {
		ctx := context.Background()
		payload := "cassette passphrase payload"

		sent, err := Send(ctx, client, SendOptions{
			Text:       payload,
			Passphrase: "cassette recording passphrase",
			TTL:        MinTTL,
			Identity:   tr.Identity("sender"),
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if sent.Model != "passphrase" {
			t.Fatalf("model = %q, want passphrase", sent.Model)
		}
		link, err := ParseShareLink(sent.Link)
		if err != nil {
			t.Fatalf("parse share link: %v", err)
		}
		if link.Identity != nil {
			t.Fatal("a model A link carries no identity")
		}

		opened, err := Open(ctx, client, OpenOptions{
			Link:       link,
			Passphrase: func() (string, error) { return "cassette recording passphrase", nil },
			OutputDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(opened.Plaintext) != payload {
			t.Errorf("opened %q, want %q", opened.Plaintext, payload)
		}
	})
}

func TestCassetteSendOpenFile(t *testing.T) {
	withTimeout(t, 2*time.Minute)
	withCassette(t, "send-open-file", func(t *testing.T, client *Client, tr *cassetteTransport) {
		ctx := context.Background()
		payload := "cassette file payload\nwith two lines\n"

		sent, err := Send(ctx, client, SendOptions{
			FileName: "cassette.txt",
			File:     strings.NewReader(payload),
			TTL:      MinTTL,
			Identity: tr.Identity("sender"),
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if sent.Uploaded {
			t.Fatal("the recording assumed the inline path; keep the file under the inline limit")
		}
		link, err := ParseShareLink(sent.Link)
		if err != nil {
			t.Fatalf("parse share link: %v", err)
		}

		opened, err := Open(ctx, client, OpenOptions{Link: link, OutputDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if filepath.Base(opened.WrittenPath) != "cassette.txt" {
			t.Errorf("written = %q, want the decrypted name", opened.WrittenPath)
		}
		got, err := os.ReadFile(opened.WrittenPath)
		if err != nil {
			t.Fatalf("read written file: %v", err)
		}
		if string(got) != payload {
			t.Errorf("file contents = %q", got)
		}
	})
}

func TestCassetteSendOpenCredential(t *testing.T) {
	withTimeout(t, 2*time.Minute)
	withCassette(t, "send-open-credential", func(t *testing.T, client *Client, tr *cassetteTransport) {
		ctx := context.Background()

		sent, err := Send(ctx, client, SendOptions{
			Credential: `{"v":1,"type":"login","fields":[{"id":"username","label":"Username","value":"cassette-user","secret":false},{"id":"password","label":"Password","value":"cassette-pass","secret":true}]}`,
			TTL:        MinTTL,
			Identity:   tr.Identity("sender"),
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		link, err := ParseShareLink(sent.Link)
		if err != nil {
			t.Fatalf("parse share link: %v", err)
		}

		opened, err := Open(ctx, client, OpenOptions{Link: link, OutputDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if opened.Credential == nil {
			t.Fatalf("a credential secret opened as plain text: %q", opened.Plaintext)
		}
		if len(opened.Credential.Fields) != 2 || opened.Credential.Fields[1].Value != "cassette-pass" {
			t.Errorf("credential = %+v", opened.Credential)
		}
	})
}

func TestCassetteRequestFlow(t *testing.T) {
	withTimeout(t, 2*time.Minute)
	withCassette(t, "request-flow", func(t *testing.T, client *Client, tr *cassetteTransport) {
		ctx := context.Background()

		created, err := CreateRequest(ctx, client, RequestOptions{
			TTL:          MinTTL,
			Note:         "cassette requester note",
			Identity:     tr.Identity("requester"),
			MemoIdentity: tr.Identity("memo"),
		})
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		subLink, err := ParseSubmissionLink(created.SubmissionLink)
		if err != nil {
			t.Fatalf("parse submission link: %v", err)
		}
		if subLink.MemoIdentity == nil {
			t.Fatal("the submission link carries no memo key")
		}
		retLink, err := ParseRetrievalLink(created.RetrievalLink)
		if err != nil {
			t.Fatalf("parse retrieval link: %v", err)
		}
		if retLink.Identity == nil {
			t.Fatal("the retrieval link carries no identity")
		}

		submitted, err := SubmitToRequest(ctx, client, SubmitOptions{Link: subLink, Text: "cassette reply"})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if submitted.State != "submitted" || submitted.Note != "cassette requester note" {
			t.Fatalf("submit result = %+v", submitted)
		}

		status, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir(), StatusOnly: true})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.State != "submitted" {
			t.Fatalf("state = %q, want submitted", status.State)
		}

		received, err := ReceiveRequest(ctx, client, ReceiveOptions{Link: retLink, OutputDir: t.TempDir()})
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if received.Opened == nil || string(received.Opened.Plaintext) != "cassette reply" {
			t.Fatalf("received = %+v", received.Opened)
		}
	})
}
