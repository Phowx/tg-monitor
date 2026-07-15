package adminapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const maxAdminBodyBytes int64 = 64 << 10

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, errorEnvelope{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func requireMethod(writer http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	writer.Header().Set("Allow", method)
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	return false
}

func requireOrigin(writer http.ResponseWriter, request *http.Request, origin string) bool {
	if request.Header.Get("Origin") == origin {
		return true
	}
	writeError(writer, http.StatusForbidden, "forbidden_origin", "request origin is not allowed")
	return false
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxAdminBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeAdminDecodeError(writer, err)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeAdminDecodeError(writer, err)
		return false
	}
	return true
}

func writeAdminDecodeError(writer http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 64 KiB")
		return
	}
	writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(value)
}
