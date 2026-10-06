package httpapi

import "net/http"

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}
