package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

func (router *Router) decodeJSON(response http.ResponseWriter, request *http.Request, target any, limit int64) bool {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确", false, nil)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求只能包含一个 JSON 对象", false, nil)
		return false
	}
	return true
}

func (router *Router) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func formatInteger(value int) string { return strconv.Itoa(value) }
