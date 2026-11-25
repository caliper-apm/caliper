package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/mcp"
)

// caliper-mcp bridges standard MCP JSON-RPC 2.0 over stdio (for Cursor, Claude Code,
// Claude Desktop, and Antigravity) to a running Caliper APM server's POST /v1/mcp endpoint.
func main() {
	serverURL := flag.String("server", envOrDefault("CALIPER_SERVER_URL", "http://127.0.0.1:8080"), "Caliper APM server base URL")
	apiKey := flag.String("api-key", os.Getenv("CALIPER_API_KEY"), "Optional X-Caliper-Key for authenticated servers")
	timeout := flag.Duration("timeout", 20*time.Second, "HTTP request timeout per MCP tool call")
	flag.Parse()

	endpoint := strings.TrimRight(strings.TrimSpace(*serverURL), "/") + "/v1/mcp"
	client := &http.Client{Timeout: *timeout}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(os.Stdout)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req mcp.RPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeLine(writer, mcp.RPCResponse{
				JSONRPC: "2.0",
				Error:   &mcp.RPCError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(line))
		if err != nil {
			if len(req.ID) > 0 {
				writeLine(writer, mcp.RPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &mcp.RPCError{Code: -32603, Message: err.Error()},
				})
			}
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json, text/event-stream")
		httpReq.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion)
		if *apiKey != "" {
			httpReq.Header.Set("X-Caliper-Key", *apiKey)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			if len(req.ID) > 0 {
				writeLine(writer, mcp.RPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &mcp.RPCError{Code: -32000, Message: fmt.Sprintf("caliper server unreachable (%s): %v", endpoint, err)},
				})
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 300 && len(bytes.TrimSpace(body)) == 0) {
			continue
		}
		if readErr != nil || resp.StatusCode >= 400 {
			if len(req.ID) > 0 {
				msg := strings.TrimSpace(string(body))
				if msg == "" && readErr != nil {
					msg = readErr.Error()
				}
				writeLine(writer, mcp.RPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &mcp.RPCError{Code: -32001, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, msg)},
				})
			}
			continue
		}

		_, _ = writer.Write(bytes.TrimSpace(body))
		_ = writer.WriteByte('\n')
		_ = writer.Flush()
	}
}

func writeLine(w *bufio.Writer, resp mcp.RPCResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = w.Write(b)
	_ = w.WriteByte('\n')
	_ = w.Flush()
}

func envOrDefault(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}
