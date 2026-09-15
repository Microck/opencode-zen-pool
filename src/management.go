package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

type managementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

func managementJSON(status int, data any) []byte {
	body, err := json.Marshal(data)
	if err != nil {
		return errorEnvelope("zen_encoding_error", "Management response unavailable", 500)
	}
	return okEnvelope(managementResponse{status, http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, body})
}
func (a *application) management(raw []byte) []byte {
	var req struct {
		Method, Path string
		Body         []byte
	}
	if json.Unmarshal(raw, &req) != nil {
		return managementJSON(400, map[string]string{"error": "invalid request"})
	}
	switch {
	case req.Method == "GET" && req.Path == a.managementBase+"/status":
		status := a.p.Status()
		return managementJSON(200, status)
	case req.Method == "POST" && req.Path == a.managementBase+"/resume":
		if len(req.Body) > 4096 {
			return managementJSON(400, map[string]string{"error": "invalid resume body"})
		}
		var body struct {
			Account string `json:"account"`
			Confirm string `json:"confirm"`
		}
		dec := json.NewDecoder(bytes.NewReader(req.Body))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF {
			return managementJSON(400, map[string]string{"error": "invalid resume body"})
		}
		if err := a.p.Resume(body.Account, body.Confirm); err != nil {
			return managementJSON(409, map[string]string{"error": err.Error()})
		}
		return managementJSON(200, map[string]string{"result": "cooldown cleared; healthy current account was not preempted"})
	default:
		return managementJSON(404, map[string]string{"error": "route not found"})
	}
}
