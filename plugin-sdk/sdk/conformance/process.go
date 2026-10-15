package conformance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

func request(method string, params any) []byte {
	raw, _ := json.Marshal(params)
	data, _ := json.Marshal(sdk.Request{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: method, Params: raw})
	return data
}

func (s suite) call(method string, params any) ([]byte, error) {
	response, err := s.exchange(request(method, params), "2")
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

func (s suite) expectError(name string, raw []byte, code int, id string) error {
	response, err := s.exchange(raw, id)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if response.Error == nil || response.Error.Code != code {
		return fmt.Errorf("%s: expected JSON-RPC error %d, got %+v", name, code, response)
	}
	return nil
}

func (s suite) exchange(raw []byte, id string) (sdk.Response, error) {
	before, err := treeHash(s.sandbox)
	if err != nil {
		return sdk.Response{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	// The host starts a fresh process, performs a handshake, then sends the method.
	hello := []byte(`{"jsonrpc":"2.0","id":1,"method":"handshake"}`)
	input := append(append(hello, '\n'), raw...)
	input = append(input, '\n')
	cmd := command(ctx, s.binary, s.root)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	after, hashErr := treeHash(s.sandbox)
	if hashErr != nil {
		return sdk.Response{}, fmt.Errorf("tree changed or became unreadable: %w", hashErr)
	}
	if before != after {
		return sdk.Response{}, errors.New("plugin changed the working tree during a protocol call")
	}
	if ctx.Err() != nil {
		return sdk.Response{}, ctx.Err()
	}
	if runErr != nil {
		return sdk.Response{}, fmt.Errorf("plugin process: %w (%s)", runErr, stderr.String())
	}
	reader := bufio.NewReader(bytes.NewReader(stdout.Bytes()))
	first, err := readResponse(reader, "1")
	if err != nil {
		return sdk.Response{}, err
	}
	if first.Error != nil {
		return sdk.Response{}, first.Error
	}
	if _, err := handshake(first.Result); err != nil {
		return sdk.Response{}, err
	}
	response, err := readResponse(reader, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := reader.Peek(1); !errors.Is(err, io.EOF) {
		return sdk.Response{}, errors.New("unexpected extra response bytes")
	}
	return response, nil
}

func readResponse(reader *bufio.Reader, id string) (sdk.Response, error) {
	var response sdk.Response
	raw, err := sdk.ReadMessage(reader)
	if err != nil {
		return response, err
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, err
	}
	if response.JSONRPC != "2.0" || string(response.ID) != id {
		return response, errors.New("invalid JSON-RPC version or response ID")
	}
	hasResult := len(response.Result) > 0
	if hasResult == (response.Error != nil) || hasResult && bytes.Equal(response.Result, []byte("null")) {
		return response, errors.New("response must contain exactly one non-null result or error")
	}
	if response.Error != nil && (response.Error.Code == 0 || strings.TrimSpace(response.Error.Message) == "") {
		return response, errors.New("invalid JSON-RPC error shape")
	}
	return response, nil
}

func command(ctx context.Context, binary, root string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = root
	cmd.Env = []string{"SPINUP_PLUGIN_API=1"}
	for _, key := range []string{"PATH", "HOME", "LANG", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.WaitDelay = time.Second
	return cmd
}

func (s suite) timeoutProbe() error {
	before, err := treeHash(s.sandbox)
	if err != nil {
		return err
	}
	// An incomplete frame must wait for input. The deadline must stop that wait.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	stop := context.AfterFunc(ctx, func() { _ = input.CloseWithError(ctx.Err()) })
	defer stop()
	cmd := command(ctx, s.binary, s.root)
	cmd.Stdin = input
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || err == nil || stdout.Len() != 0 {
		return errors.New("timeout: incomplete request did not wait for cancellation")
	}
	after, err := treeHash(s.sandbox)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("timeout: plugin changed the working tree")
	}
	return nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	// A call contains two bounded responses, including their newlines.
	if b.Len()+len(data) > 2*(sdk.MaxMessageBytes+1) {
		return 0, errors.New("plugin output exceeds response limit")
	}
	return b.Buffer.Write(data)
}

func treeHash(root string) ([32]byte, error) {
	hash := sha256.New()
	err := fs.WalkDir(os.DirFS(root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%d:%s:%o:%d:", len(name), name, info.Mode(), info.Size())
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(root + string(os.PathSeparator) + name)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
		case info.Mode().IsRegular():
			file, err := os.Open(root + string(os.PathSeparator) + name)
			if err != nil {
				return err
			}
			_, readErr := io.Copy(hash, file)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			return closeErr
		case !entry.IsDir():
			return fmt.Errorf("unsupported tree entry %q", name)
		}
		return nil
	})
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return sum, err
}
