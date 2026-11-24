package flickcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Felix-LeeSM/flick-drop/internal/clientcrypto"
)

// Live transfer over /api/pipe/*: a short-lived room keyed by a 6-digit code,
// relayed through server memory (docs/architecture/live-relay.md). The
// receiver generates a fresh age key per attempt; the sender encrypts to the
// recipient string the server shows with the sender token. Credentials are
// authenticated with header tokens, never query strings.

const (
	pipeCodeHeader   = "X-Flick-Pipe-Code"
	pipeSenderHeader = "X-Flick-Pipe-Sender"
	pipeConnHeader   = "X-Flick-Pipe-Connection"
)

var pipeConfirmPattern = regexp.MustCompile(`^[0-9]{3}$`)

// Dashes and spaces are ignored, like piperelay.NormalizeCode on the server.
func NormalizePipeCode(raw string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	if len(cleaned) != 6 || strings.Trim(cleaned, "0123456789") != "" {
		return "", errors.New("a live transfer code is six digits")
	}
	return cleaned, nil
}

func (c *Client) pipeRequest(ctx context.Context, method, path string, headers map[string]string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", c.baseURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func (c *Client) pipeJSON(ctx context.Context, method, path string, headers map[string]string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
		headers["Content-Type"] = "application/json"
	}
	resp, err := c.pipeRequest(ctx, method, path, headers, reader)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

type PipeSession struct {
	Code        string
	SenderToken string
	ExpiresIn   int
}

func (c *Client) PipeInit(ctx context.Context) (PipeSession, error) {
	var resp struct {
		Code        string `json:"code"`
		ExpiresIn   int    `json:"expires_in"`
		SenderToken string `json:"sender_token"`
	}
	err := c.pipeJSON(ctx, http.MethodPost, "/api/pipe/init", map[string]string{}, struct{}{}, &resp)
	return PipeSession{Code: resp.Code, SenderToken: resp.SenderToken, ExpiresIn: resp.ExpiresIn}, err
}

type PipeJoined struct {
	ConnectionID string
	VerifyCode   string
	Device       string
	ExpiresIn    int
}

func (c *Client) PipeJoin(ctx context.Context, code, recipient string) (PipeJoined, error) {
	var resp struct {
		ConnectionID string `json:"connection_id"`
		VerifyCode   string `json:"verify_code"`
		Device       string `json:"device"`
		ExpiresIn    int    `json:"expires_in"`
	}
	headers := map[string]string{pipeCodeHeader: code}
	err := c.pipeJSON(ctx, http.MethodPost, "/api/pipe/join", headers, map[string]string{"recipient": recipient}, &resp)
	return PipeJoined{ConnectionID: resp.ConnectionID, VerifyCode: resp.VerifyCode, Device: resp.Device, ExpiresIn: resp.ExpiresIn}, err
}

// Long-poll: the server holds until the status changes or about ten seconds pass.
func (c *Client) PipeStatus(ctx context.Context, code, connectionID string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	headers := map[string]string{pipeCodeHeader: code, pipeConnHeader: connectionID}
	err := c.pipeJSON(ctx, http.MethodGet, "/api/pipe/status", headers, nil, &resp)
	return resp.Status, err
}

type PipeStream struct {
	Kind              string
	EncryptedFilename string
	Body              io.ReadCloser
}

// The live stream: raw age ciphertext; once opened, this response is the only copy.
func (c *Client) PipeReceive(ctx context.Context, code, connectionID string) (PipeStream, error) {
	headers := map[string]string{pipeCodeHeader: code, pipeConnHeader: connectionID}
	resp, err := c.pipeRequest(ctx, http.MethodGet, "/api/pipe/receive", headers, nil)
	if err != nil {
		return PipeStream{}, err
	}
	stream := PipeStream{
		Kind:              resp.Header.Get("Flick-Kind"),
		EncryptedFilename: resp.Header.Get("Flick-Encrypted-Filename"),
		Body:              resp.Body,
	}
	if stream.Kind != "text" && stream.Kind != "file" {
		resp.Body.Close()
		return PipeStream{}, fmt.Errorf("live transfer response has an unknown kind %q", stream.Kind)
	}
	return stream, nil
}

type PipePending struct {
	ConnectionID     string
	Index            int
	VerifyCode       string
	Device           string
	Recipient        string
	ConnectedSeconds int
}

type PipeWaitResult struct {
	Status      string
	Version     int
	Recipient   string
	Connections []PipePending
}

// Long-poll: the server holds until the room version moves past since or about
// twenty-five seconds pass.
func (c *Client) PipeWait(ctx context.Context, code, senderToken string, since int) (PipeWaitResult, error) {
	var resp struct {
		Status      string `json:"status"`
		Version     int    `json:"version"`
		Recipient   string `json:"recipient"`
		Connections []struct {
			ConnectionID     string `json:"connection_id"`
			Index            int    `json:"index"`
			VerifyCode       string `json:"verify_code"`
			Device           string `json:"device"`
			Recipient        string `json:"recipient"`
			ConnectedSeconds int    `json:"connected_seconds"`
		} `json:"connections"`
	}
	headers := map[string]string{pipeCodeHeader: code, pipeSenderHeader: senderToken}
	err := c.pipeJSON(ctx, http.MethodGet, "/api/pipe/wait?since="+url.QueryEscape(fmt.Sprint(since)), headers, nil, &resp)
	if err != nil {
		return PipeWaitResult{}, err
	}
	out := PipeWaitResult{Status: resp.Status, Version: resp.Version, Recipient: resp.Recipient}
	for _, conn := range resp.Connections {
		out.Connections = append(out.Connections, PipePending{
			ConnectionID:     conn.ConnectionID,
			Index:            conn.Index,
			VerifyCode:       conn.VerifyCode,
			Device:           conn.Device,
			Recipient:        conn.Recipient,
			ConnectedSeconds: conn.ConnectedSeconds,
		})
	}
	return out, nil
}

func (c *Client) PipeSelect(ctx context.Context, code, senderToken, connectionID string) error {
	headers := map[string]string{pipeCodeHeader: code, pipeSenderHeader: senderToken, pipeConnHeader: connectionID}
	return c.pipeJSON(ctx, http.MethodPost, "/api/pipe/select", headers, nil, nil)
}

func (c *Client) PipeDecline(ctx context.Context, code, senderToken, connectionID string) error {
	headers := map[string]string{pipeCodeHeader: code, pipeSenderHeader: senderToken, pipeConnHeader: connectionID}
	return c.pipeJSON(ctx, http.MethodPost, "/api/pipe/decline", headers, nil, nil)
}

func (c *Client) PipeLeave(ctx context.Context, code, connectionID string) error {
	headers := map[string]string{pipeCodeHeader: code, pipeConnHeader: connectionID}
	return c.pipeJSON(ctx, http.MethodPost, "/api/pipe/leave", headers, nil, nil)
}

func (c *Client) PipeCancel(ctx context.Context, code, senderToken string) error {
	headers := map[string]string{pipeCodeHeader: code, pipeSenderHeader: senderToken}
	return c.pipeJSON(ctx, http.MethodPost, "/api/pipe/cancel", headers, nil, nil)
}

// The body is raw age ciphertext encrypted to the chosen receiver; the server
// waits for that receiver to open /receive and then streams the bytes through.
func (c *Client) PipeSend(ctx context.Context, code, senderToken, kind, encryptedFilename string, body io.Reader, size int64) (int64, error) {
	headers := map[string]string{
		pipeCodeHeader:             code,
		pipeSenderHeader:           senderToken,
		"Content-Type":             "application/octet-stream",
		"Flick-Kind":               kind,
		"Flick-Encrypted-Filename": encryptedFilename,
	}
	resp, err := c.pipeRequest(ctx, http.MethodPost, "/api/pipe/send", headers, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Status           string `json:"status"`
		BytesTransferred int64  `json:"bytes_transferred"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response from /api/pipe/send: %w", err)
	}
	return out.BytesTransferred, nil
}

// A decision on the waiting receivers: "send" to a connection, "decline" one,
// or "wait" for the next room update.
type LiveDecision struct {
	Action       string
	ConnectionID string
}

type LiveSendOptions struct {
	Text       string
	Credential string
	FileName   string
	File       io.Reader
	// Pick is consulted whenever receivers are waiting; nil sends to the
	// lowest-index row (the -confirm auto mode).
	Pick func(pending []PipePending) (LiveDecision, error)
	// Started reports the code as soon as the room exists.
	Started func(PipeSession) error
}

type LiveSendResult struct {
	Code             string
	ConnectionID     string
	VerifyCode       string
	BytesTransferred int64
}

func LiveSend(ctx context.Context, client *Client, opts LiveSendOptions) (LiveSendResult, error) {
	if opts.Credential != "" {
		cred, err := ParseCredentialInput([]byte(opts.Credential))
		if err != nil {
			return LiveSendResult{}, err
		}
		opts.Text = SerializeCredential(cred)
		opts.Credential = ""
	}
	switch {
	case opts.Text != "" && opts.FileName != "":
		return LiveSendResult{}, errors.New("send text or a file, not both")
	case opts.Credential != "" && opts.FileName != "":
		return LiveSendResult{}, errors.New("send a credential or a file, not both")
	case opts.Text == "" && opts.Credential == "" && opts.FileName == "":
		return LiveSendResult{}, errors.New("nothing to send: provide text, a credential, or a file")
	case opts.FileName != "" && opts.File == nil:
		return LiveSendResult{}, errors.New("no file contents to send")
	}

	// The CLI receiver refuses text past the inline limit, so the sender never
	// mints a room for a payload it cannot deliver to another CLI device.
	if opts.FileName == "" {
		if limit := client.Config(ctx).PayloadInlineMaxBytes; limit > 0 && int64(len(opts.Text)) > limit {
			return LiveSendResult{}, fmt.Errorf("text is too large (%d bytes; inline limit is %d bytes): send as a file with -file instead", len(opts.Text), limit)
		}
	}

	session, err := client.PipeInit(ctx)
	if err != nil {
		return LiveSendResult{}, err
	}
	if opts.Started != nil {
		if err := opts.Started(session); err != nil {
			client.cancelPipeSession(session)
			return LiveSendResult{Code: session.Code}, err
		}
	}

	since := 0
	selected := false
	var chosen PipePending
	for {
		wait, err := client.PipeWait(ctx, session.Code, session.SenderToken, since)
		if err != nil {
			if ctx.Err() != nil {
				client.cancelPipeSession(session)
				return LiveSendResult{Code: session.Code}, ctx.Err()
			}
			return LiveSendResult{Code: session.Code}, err
		}
		since = wait.Version
		if selected {
			if wait.Status == "ready" {
				recipient := wait.Recipient
				if recipient == "" {
					recipient = chosen.Recipient
				}
				return client.sendLivePayload(ctx, session, opts, chosen, recipient)
			}
			continue
		}
		if len(wait.Connections) == 0 {
			continue
		}

		decision := LiveDecision{Action: "send", ConnectionID: lowestPipePending(wait.Connections)}
		if opts.Pick != nil {
			if decision, err = opts.Pick(wait.Connections); err != nil {
				client.cancelPipeSession(session)
				return LiveSendResult{Code: session.Code}, err
			}
		}
		switch decision.Action {
		case "wait":
		case "decline":
			if err := client.PipeDecline(ctx, session.Code, session.SenderToken, decision.ConnectionID); err != nil {
				// A row that already left is not an error; the room keeps working.
				if !pipeRowAlreadyGone(err) {
					return LiveSendResult{Code: session.Code}, err
				}
			}
		case "send":
			found, ok := findPipePending(wait.Connections, decision.ConnectionID)
			if !ok {
				continue
			}
			if err := client.PipeSelect(ctx, session.Code, session.SenderToken, found.ConnectionID); err != nil {
				if !pipeRowAlreadyGone(err) {
					return LiveSendResult{Code: session.Code}, err
				}
				continue
			}
			selected = true
			chosen = found
		}
	}
}

func (c *Client) sendLivePayload(ctx context.Context, session PipeSession, opts LiveSendOptions, chosen PipePending, recipient string) (LiveSendResult, error) {
	result := LiveSendResult{Code: session.Code, ConnectionID: chosen.ConnectionID, VerifyCode: chosen.VerifyCode}
	limits := c.Config(ctx)

	if opts.FileName != "" {
		name, err := sealToRecipient(recipient, []byte(opts.FileName))
		if err != nil {
			return result, fmt.Errorf("encrypt filename: %w", err)
		}
		sealed, err := sealFileToRecipient(recipient, opts.File, limits.MaxFileBytes)
		if err != nil {
			return result, err
		}
		defer sealed.remove()
		result.BytesTransferred, err = c.PipeSend(ctx, session.Code, session.SenderToken, "file",
			base64.StdEncoding.EncodeToString(name), sealed.file, sealed.size)
		return result, err
	}

	sealed, err := sealToRecipient(recipient, []byte(opts.Text))
	if err != nil {
		return result, fmt.Errorf("encrypt payload: %w", err)
	}
	result.BytesTransferred, err = c.PipeSend(ctx, session.Code, session.SenderToken, "text", "",
		bytes.NewReader(sealed), int64(len(sealed)))
	return result, err
}

// not_pending is the documented race when a receiver leaves between the wait
// response and the sender action; every other failure ends the session.
func pipeRowAlreadyGone(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == "not_pending"
}

func lowestPipePending(pending []PipePending) string {
	lowest := pending[0]
	for _, conn := range pending[1:] {
		if conn.Index < lowest.Index {
			lowest = conn
		}
	}
	return lowest.ConnectionID
}

func findPipePending(pending []PipePending, connectionID string) (PipePending, bool) {
	for _, conn := range pending {
		if conn.ConnectionID == connectionID {
			return conn, true
		}
	}
	return PipePending{}, false
}

type LiveJoinOptions struct {
	Code       string
	OutputPath string
	OutputDir  string
	Reveal     bool
	// Connected reports the 3-digit number the receiver reads to the sender.
	Connected func(verifyCode string, expiresIn int) error
}

// The receiving half: a fresh identity per attempt, the verify number out-of-band,
// then the consuming stream decrypted under the same destination rules as an open.
func LiveJoin(ctx context.Context, client *Client, opts LiveJoinOptions) (OpenResult, error) {
	identity, err := clientcrypto.NewIdentity()
	if err != nil {
		return OpenResult{}, err
	}
	joined, err := client.PipeJoin(ctx, opts.Code, clientcrypto.RecipientStringOf(identity))
	if err != nil {
		return OpenResult{}, err
	}
	if opts.Connected != nil {
		if err := opts.Connected(joined.VerifyCode, joined.ExpiresIn); err != nil {
			client.leavePipeSession(opts.Code, joined.ConnectionID)
			return OpenResult{}, err
		}
	}
	for {
		status, err := client.PipeStatus(ctx, opts.Code, joined.ConnectionID)
		if err != nil {
			if ctx.Err() != nil {
				client.leavePipeSession(opts.Code, joined.ConnectionID)
				return OpenResult{}, ctx.Err()
			}
			return OpenResult{}, err
		}
		if status == "selected" {
			break
		}
	}

	stream, err := client.PipeReceive(ctx, opts.Code, joined.ConnectionID)
	if err != nil {
		return OpenResult{}, err
	}
	defer stream.Body.Close()

	limits := client.Config(ctx)
	openOpts := OpenOptions{OutputPath: opts.OutputPath, OutputDir: opts.OutputDir, Reveal: opts.Reveal}
	if stream.Kind == "file" {
		reserved, err := reserveDestination(openOpts, "file", "")
		if err != nil {
			return OpenResult{}, err
		}
		defer reserved.close()
		return openFile(openOpts, reserved, identity, stream.EncryptedFilename, stream.Body, OpenResult{Kind: "file"})
	}

	reserved, err := reserveDestination(openOpts, "text", "flick-live-"+opts.Code)
	if err != nil {
		return OpenResult{}, err
	}
	defer reserved.close()
	capped := &countingReader{r: stream.Body, limit: ageFileBytes(limits.PayloadInlineMaxBytes)}
	result, err := openText(openOpts, reserved, identity, capped, OpenResult{Kind: "text"})
	if err != nil {
		// The result carries the plaintext or rescued path for the caller to salvage.
		if errors.Is(err, ErrWriteAfterConsume) {
			return result, err
		}
		return result, err
	}
	if capped.over {
		// The stream was received and secured; surface it with the cap notice.
		return result, ErrOverInlineCap
	}
	return result, nil
}

// After the process is leaving: short budgets, errors swallowed, never retried.
func (c *Client) cancelPipeSession(session PipeSession) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.PipeCancel(ctx, session.Code, session.SenderToken)
}

func (c *Client) leavePipeSession(code, connectionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.PipeLeave(ctx, code, connectionID)
}
