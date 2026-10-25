// Package mcp is a minimal Model Context Protocol server over stdio: enough
// of the protocol (initialize, ping, tools/list, tools/call) for Claude Code
// and Codex to call spindle's memory tools. Messages are newline-delimited
// JSON-RPC 2.0.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Tool is one callable tool.
type Tool struct {
	Name        string                                                          `json:"name"`
	Description string                                                          `json:"description"`
	InputSchema json.RawMessage                                                 `json:"inputSchema"`
	Call        func(ctx context.Context, args json.RawMessage) (string, error) `json:"-"`
}

// Server serves a fixed tool set.
type Server struct {
	Name, Version string
	Tools         []Tool
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// protocolVersions the server can speak, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Serve reads requests from in until EOF and writes responses to out.
func (s Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var mu sync.Mutex
	enc := json.NewEncoder(out)
	write := func(r response) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(r)
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification, e.g. notifications/initialized
		}
		result, rerr := s.handle(ctx, req)
		write(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rerr})
	}
	return sc.Err()
}

func (s Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range s.Tools {
			if t.Name != p.Name {
				continue
			}
			args := p.Arguments
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			text, err := t.Call(ctx, args)
			if err != nil {
				// Tool failures are results the model can read, not protocol errors.
				return toolResult(fmt.Sprintf("error: %v", err), true), nil
			}
			return toolResult(text, false), nil
		}
		return nil, &rpcError{-32602, "unknown tool " + p.Name}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
