// Package mcp serves a project's memory tools over stdio with the Model
// Context Protocol: newline-delimited JSON-RPC 2.0, and only the methods the
// tools need (initialize, ping, tools/list, tools/call).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Serve answers requests from in until EOF.
func (s Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &Error{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification
		}
		result, err := s.handle(ctx, req)
		_ = enc.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: err})
	}
	return sc.Err()
}

func (s Server) handle(ctx context.Context, req request) (any, *Error) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p) // missing or bad params get the default version
		v := versions[0]
		for _, x := range versions {
			if x == p.ProtocolVersion {
				v = x
			}
		}
		return map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": s.Name, "version": s.Version}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return nil, &Error{-32602, "invalid params"}
		}
		for _, t := range s.Tools {
			if t.Name == p.Name {
				args := p.Arguments
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				text, err := t.Call(ctx, args)
				if err != nil {
					return result(fmt.Sprintf("error: %v", err), true), nil
				}
				return result(text, false), nil
			}
		}
		return nil, &Error{-32602, "unknown tool " + p.Name}
	}
	return nil, &Error{-32601, "method not found: " + req.Method}
}

func result(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}
