package tavilyapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

func writeSearchEndpointResponse(
	w http.ResponseWriter,
	response SearchResponse,
	err error,
) {
	if err != nil {
		status := rawContentResponseStatus(err)
		if errors.Is(err, errSearchUnavailable) {
			w.Header().Set("Retry-After", "1")
		}
		writeError(w, status, err.Error())
		return
	}

	if response.private {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if response.personalImpression != "" {
		w.Header().Set("X-Yago-Impression", response.personalImpression)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}
